package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis/v9"
	"github.com/greymatter-io/operator/pkg/cuemodule"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/tidwall/gjson"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type SyncState struct {
	ctx           context.Context
	redisHost     string
	redis         *redis.Client
	saveGMHashes  chan interface{}
	saveK8sHashes chan interface{}

	previousGMHashes  map[string]GMObjectRef  // no lock because we only replace the whole map at once
	previousK8sHashes map[string]K8sObjectRef // no lock because we only replace the whole map at once
}

// GMObjectRef contains enough information to know whether an object has changed, and delete it if removed
type GMObjectRef struct {
	// The name of the Grey Matter zone to which this object belongs
	Zone string `json:"zone"`
	// domain, listener, route, cluster, proxy, zone, or catalogservice
	Kind string `json:"kind"`
	// The contents of this object's domain_key, listener_key, route_key, cluster_key, proxy_key, zone_key, or service_id
	// based on the object kind (stored separately, below)
	ID string `json:"id"`
	// A deterministic hash of the source object content
	Hash uint64 `json:"hash"`
}

func NewGMObjectRef(objBytes []byte, kind string) *GMObjectRef {
	// TODO confirm that the []byte representation maps 1-1 with the original object (i.e., no key rearrangement)
	//      if it *doesn't*, then we need to rehydrate the object before hashing below
	keyName := cuemodule.KindToKeyName[kind] // One of listener_key, proxy_key, etc., so we can look up the ID
	var zoneLookupKey string
	if kind == "catalogservice" {
		zoneLookupKey = "mesh_id" // for catalogservice, we store the mesh_id in the zone
	} else {
		zoneLookupKey = "zone_key"
	}
	zoneResult := gjson.GetBytes(objBytes, zoneLookupKey)
	idResult := gjson.GetBytes(objBytes, keyName)
	hash, _ := hashstructure.Hash(objBytes, hashstructure.FormatV2, nil)
	return &GMObjectRef{
		Zone: zoneResult.String(),
		Kind: kind,
		ID:   idResult.String(),
		Hash: hash,
	}
}
func (obj *GMObjectRef) HashKey() (key string) {
	// A properly-namespaced key for the object that should uniquely identify it
	return fmt.Sprintf("%s-%s-%s", obj.Zone, obj.Kind, obj.ID)
}

// FilterChangedGM takes Grey Matter config objects and their kinds, and returned filtered versions of those lists
// which don't contain any objects that are the same since the last update, as well as updating the stored hashes as a
// side effect. The purpose is to return only objects that need to be applied to the environment.
func (ss *SyncState) FilterChangedGM(configObjects []json.RawMessage, kinds []string) (filteredConf []json.RawMessage, filteredKinds []string, deleted []GMObjectRef) {
	newHashes := make(map[string]GMObjectRef)
	for i, objBytes := range configObjects {
		val := NewGMObjectRef(objBytes, kinds[i])
		key := val.HashKey()

		newHashes[key] = *val
		if prevVal, ok := ss.previousGMHashes[key]; !ok || prevVal.Hash != val.Hash {
			filteredConf = append(filteredConf, objBytes)
			filteredKinds = append(filteredKinds, val.Kind)
		}
	}

	// find deleted
	for oldKey, oldVal := range ss.previousGMHashes {
		if _, ok := newHashes[oldKey]; !ok {
			deleted = append(deleted, oldVal)
		}
	}

	// save new hash table
	ss.previousGMHashes = newHashes
	go func() { ss.saveGMHashes <- struct{}{} }() // asynchronously kick-off asynchronous persistence
	return
}

type K8sObjectRef struct {
	Namespace string                  `json:"namespace"`
	Kind      schema.GroupVersionKind `json:"kind"`
	Name      string                  `json:"name"`
	Hash      uint64                  `json:"hash"`
}

func NewK8sObjectRef(object client.Object) *K8sObjectRef {
	hash, _ := hashstructure.Hash(object, hashstructure.FormatV2, nil)
	return &K8sObjectRef{
		Namespace: object.GetNamespace(),
		Kind:      object.GetObjectKind().GroupVersionKind(),
		Name:      object.GetName(),
		Hash:      hash,
	}
}
func (obj *K8sObjectRef) HashKey() (key string) {
	// A properly-namespaced key for the object that should uniquely identify it
	return fmt.Sprintf("%s-%s-%s", obj.Namespace, obj.Kind, obj.Name)
}

// FilterChangedK8s takes Grey Matter config objects, and returns a filtered version of that list, updating the stored
// hashes as a side effect which don't contain any objects that are the same since the last update. The purpose is to
// return only objects that need to be applied to the environment.
func (ss *SyncState) FilterChangedK8s(manifestObjects []client.Object) (filtered []client.Object, deleted []K8sObjectRef) {
	newHashes := make(map[string]K8sObjectRef)
	for _, manifestObject := range manifestObjects {
		val := NewK8sObjectRef(manifestObject)
		key := val.HashKey()
		newHashes[key] = *val // store *all* of them in newHashes, to replace previousGMHashes
		// if the hashes don't match, the object has changed, and it should be in the filtered list
		if prevVal, ok := ss.previousK8sHashes[key]; !ok || prevVal.Hash != val.Hash {
			filtered = append(filtered, manifestObject)
		}
	}
	// find deleted
	for oldKey, oldVal := range ss.previousK8sHashes {
		if _, ok := newHashes[oldKey]; !ok {
			deleted = append(deleted, oldVal)
		}
	}

	// save new hash table
	ss.previousK8sHashes = newHashes
	go func() { ss.saveK8sHashes <- struct{}{} }() // asynchronously kick-off asynchronous persistence
	return
}

func newSyncState(defaults cuemodule.Defaults) *SyncState {
	ctx := context.Background() // TODO inject external context

	ss := &SyncState{
		ctx:               ctx,
		redisHost:         defaults.RedisHost,
		redis:             nil, // Filled later by .redisConnect()
		saveGMHashes:      make(chan interface{}),
		saveK8sHashes:     make(chan interface{}),
		previousGMHashes:  make(map[string]GMObjectRef),
		previousK8sHashes: make(map[string]K8sObjectRef),
	}

	// immediately attempt to connect to Redis
	err := ss.redisConnect()
	if err == nil {
		// if we're able to connect immediately, try to load saved GM hashes
		loadedGMHashes := make(map[string]GMObjectRef)
		resultGM := ss.redis.Get(ctx, defaults.GitOpsStateKeyGM)
		bsGM, err := resultGM.Bytes()
		if err == nil { // if NO error, unmarshall the map
			err = json.Unmarshal(bsGM, &loadedGMHashes)
			if err == nil { // also no unmarshall error
				ss.previousGMHashes = loadedGMHashes
				logger.Info("Successfully loaded GM object hashes from Redis", "key", defaults.GitOpsStateKeyGM)
			} else {
				logger.Error(err, "Problem unmarshalling GM hashes from Redis", "key", defaults.GitOpsStateKeyGM)
			}
		}
		// if we're able to connect immediately, try to load saved K8s hashes
		loadedK8sHashes := make(map[string]K8sObjectRef)
		resultK8s := ss.redis.Get(ctx, defaults.GitOpsStateKeyK8s)
		bsK8s, err := resultK8s.Bytes()
		if err == nil { // if NO error, unmarshall the map
			err = json.Unmarshal(bsK8s, &loadedK8sHashes)
			if err == nil { // also no unmarshall error
				ss.previousK8sHashes = loadedK8sHashes
				logger.Info("Successfully loaded K8s object hashes from Redis", "key", defaults.GitOpsStateKeyK8s)
			} else {
				logger.Error(err, "problem unmarshalling K8s hashes from Redis", "key", defaults.GitOpsStateKeyK8s)
			}
		}
	}

	ss.launchAsyncStateBackupLoop(ctx, defaults)

	return ss
}

func (ss *SyncState) redisConnect() error {
	if ss.redis != nil {
		return nil
	}
	// TODO don't hard-code these
	rdb := redis.NewClient(&redis.Options{
		Addr:       fmt.Sprintf("%s:%d", ss.redisHost, 6379),
		DB:         0,
		MaxRetries: -1,
		// TODO optional configurable credentials
	})
	err := rdb.Ping(ss.ctx).Err()
	if err == nil { // if NO error
		ss.redis = rdb // save client
		logger.Info("Connected to Redis for state backup")
	}
	return err
}

func (ss *SyncState) launchAsyncStateBackupLoop(ctx context.Context, defaults cuemodule.Defaults) {

	go func() {
		// first, wait for a Redis connection
	RetryRedis:
		err := ss.redisConnect()
		if err != nil {
			time.Sleep(30 * time.Second)
			logger.Info(fmt.Sprintf("Waiting another 30 seconds for Redis availability (%v)", err))
			goto RetryRedis
		}

		// then watch the update signal channels and persist the associated key to Redis
		for {
			select {
			case <-ctx.Done():
				return
			case <-ss.saveGMHashes:
				ss.persistGMHashesToRedis(ss.previousGMHashes, defaults.GitOpsStateKeyGM)
			case <-ss.saveK8sHashes:
				ss.persistK8sHashesToRedis(ss.previousK8sHashes, defaults.GitOpsStateKeyK8s)
			}
		}

	}()
}

func (ss *SyncState) persistGMHashesToRedis(hashes map[string]GMObjectRef, key string) {
	b, err := json.Marshal(hashes)
	if err != nil {
		logger.Error(err, "Failed to serialize GM environment state hashes (for backup to Redis)", "hashes", hashes)
		return
	}
	if err := ss.redis.Set(ss.ctx, key, b, 0).Err(); err != nil {
		logger.Error(err, "Failed to save GM environment state hashes to Redis", "hashes", hashes)
	}
}

func (ss *SyncState) persistK8sHashesToRedis(hashes map[string]K8sObjectRef, key string) {
	b, err := json.Marshal(hashes)
	if err != nil {
		logger.Error(err, "Failed to serialize K8s environment state hashes (for backup to Redis)", "hashes", hashes)
		return
	}
	if err := ss.redis.Set(ss.ctx, key, b, 0).Err(); err != nil {
		logger.Error(err, "Failed to save K8s environment state hashes to Redis", "hashes", hashes)
	}
}
