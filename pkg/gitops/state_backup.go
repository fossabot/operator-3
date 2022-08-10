package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v9"
	"github.com/greymatter-io/operator/pkg/cuemodule"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/tidwall/gjson"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SyncState is the machinery responsible for managing
// operator internal state.
//
// On startup a connection to redis is initialized,
// if state already exists we sync, if it doesn't we create.
//
// During operations the operator will consistently reconcile
// with redis given hashes of objects it receives from its git
// repos. If it detects changes in hashes, it updates the state and
// the subsequent control-plane with ONLY the changed objects.
type SyncState struct {
	ctx       context.Context
	redisOpts *redis.Options
	redis     *redis.Client
	saveChans map[string]chan interface{}

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
	go func() { ss.saveChans["gm"] <- struct{}{} }() // asynchronously kick-off asynchronous persistence
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
	go func() { ss.saveChans["k8s"] <- struct{}{} }() // asynchronously kick-off asynchronous persistence
	return
}

func NewSyncState(ctx context.Context, defaults cuemodule.Defaults) *SyncState {
	ss := &SyncState{
		ctx: ctx,
		redisOpts: &redis.Options{
			Addr:       fmt.Sprintf("%s:%d", defaults.RedisHost, defaults.RedisPort),
			DB:         defaults.RedisDB,
			Username:   defaults.RedisUsername,
			Password:   defaults.RedisPassword,
			MaxRetries: -1,
		},
		saveChans: map[string]chan interface{}{
			"gm":  make(chan interface{}, 1),
			"k8s": make(chan interface{}, 1),
		},
		previousGMHashes:  make(map[string]GMObjectRef),
		previousK8sHashes: make(map[string]K8sObjectRef),
	}

	// immediately attempt to connect to Redis
	err := ss.redisConnect()
	if err != nil {
		logger.Error(err, "Didn't successfully connect to redis...")
		return &SyncState{}
	}

	// if we're able to connect immediately, try to load saved GM hashes
	loadedGMHashes := make(map[string]GMObjectRef)
	resultGM := ss.redis.Get(ctx, defaults.GitOpsStateKeyGM)
	bsGM, err := resultGM.Bytes()
	if err != nil {
		logger.Error(err, "Failed to retrieve greymatter configs...")
		return &SyncState{}
	}
	if err = json.Unmarshal(bsGM, &loadedGMHashes); err != nil {
		logger.Error(err, "Problem unmarshaling GM hashes from Redis", "key", defaults.GitOpsStateKeyGM)
		return &SyncState{}
	}
	ss.previousGMHashes = loadedGMHashes
	logger.Info("Successfully loaded GM object hashes from Redis", "key", defaults.GitOpsStateKeyGM)

	// if we're able to connect immediately, try to load saved K8s hashes
	loadedK8sHashes := make(map[string]K8sObjectRef)
	resultK8s := ss.redis.Get(ctx, defaults.GitOpsStateKeyK8s)
	bsK8s, err := resultK8s.Bytes()
	if err != nil {
		logger.Error(err, "Failed to retrieve kubernetes configs...")
		return &SyncState{}
	}
	if err = json.Unmarshal(bsK8s, &loadedK8sHashes); err != nil {
		logger.Error(err, "Problem unmarshaling GM hashes from Redis", "key", defaults.GitOpsStateKeyK8s)
		return &SyncState{}
	}
	ss.previousK8sHashes = loadedK8sHashes
	logger.Info("Successfully loaded K8s object hashes from Redis", "key", defaults.GitOpsStateKeyK8s)

	// After we've successfully loaded we launch our async backup loop
	// to continue reconciliation with redis.
	ss.launchAsyncStateBackupLoop(ctx, defaults)

	return ss
}

func (ss *SyncState) redisConnect() error {
	if ss.redis != nil {
		return nil
	}

	rdb := redis.NewClient(ss.redisOpts)
	err := rdb.Ping(ss.ctx).Err()
	if err == nil { // if NO error save the client
		ss.redis = rdb
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
				logger.Info("Received done signal, closing asynchronous state backup loop...")
				return
			case <-ss.saveChans["gm"]:
				ss.persistGMHashesToRedis(ss.previousGMHashes, defaults.GitOpsStateKeyGM)
			case <-ss.saveChans["k8s"]:
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
