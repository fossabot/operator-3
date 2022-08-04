package gmapi

import (
	"encoding/json"
	"fmt"
	"github.com/greymatter-io/operator/pkg/gitops"
	"github.com/tidwall/gjson"
)

func MkApply(kind string, data json.RawMessage) Cmd {
	key := objKey(kind, data)
	return Cmd{
		args:    fmt.Sprintf("apply -t %s -f -", kind),
		requeue: true,
		stdin:   data,
		log: func(out string, err error) {
			if err != nil {
				logger.Error(fmt.Errorf(out), "failed apply", "type", kind, "key", key)
			} else {
				logger.Info("apply", "type", kind, "key", key)
			}
		},
	}
}

func ApplyAll(client *Client, objects []json.RawMessage, kinds []string) {
	for i, kind := range kinds {
		if kind == "catalogservice" { // Catalog is special, because it goes on a different channel
			client.CatalogCmds <- MkApply(kind, objects[i])
		} else if kind != "" { // Everything else goes to Control
			client.ControlCmds <- MkApply(kind, objects[i])
		} else {
			// TODO explode
			logger.Error(nil, "Loaded unexpected object, not recognizable as Grey Matter config", "Object", string(objects[i]))
		}
	}
}

func UnApplyAll(client *Client, objects []json.RawMessage, kinds []string) {
	for i, kind := range kinds {
		if kind == "catalogservice" { // Catalog is special, because it goes on a different channel
			client.CatalogCmds <- mkDelete(kind, objects[i])
		} else if kind != "" { // Everything else goes to Control
			client.ControlCmds <- mkDelete(kind, objects[i])
		} else {
			// TODO explode
			logger.Error(nil, "Loaded unexpected object, not recognizable as Grey Matter config - ignoring", "Object", string(objects[i]))
		}
	}
}

func DeleteAllByGMObjectRefs(client *Client, objectsToDelete []gitops.GMObjectRef) {
	for _, objRef := range objectsToDelete {
		if objRef.Kind == "catalogservice" { // Catalog is special, because it goes on a different channel
			client.CatalogCmds <- mkDeleteByGMObjectRef(objRef)
		} else if objRef.Kind != "" { // Everything else goes to Control
			client.ControlCmds <- mkDeleteByGMObjectRef(objRef)
		} else {
			// TODO explode
			logger.Error(nil, "Loaded unexpected object, not recognizable as Grey Matter config - ignoring", "ref", objRef)
		}
	}
}

func mkDeleteByGMObjectRef(objRef gitops.GMObjectRef) Cmd {
	args := fmt.Sprintf("delete %s --%s %s", objRef.Kind, kindFlag(objRef.Kind), objRef.ID)
	if objRef.Kind == "catalogservice" {
		// In a catalogservice object, we interpret the zone as the mesh ID
		args += fmt.Sprintf(" --mesh-id %s", objRef.Zone)
	}
	return Cmd{
		args: args,
		log: func(out string, err error) {
			if err != nil {
				logger.Error(fmt.Errorf(out), "failed delete", "type", objRef.Kind, "key", objRef.ID)
			} else {
				logger.Info("delete", "type", objRef.Kind, "key", objRef.ID)
			}
		},
	}
}

func mkDelete(kind string, data json.RawMessage) Cmd {
	key := objKey(kind, data)
	args := fmt.Sprintf("delete %s --%s %s", kind, kindFlag(kind), key)
	if kind == "catalogservice" {
		var extracted struct {
			MeshID string `json:"mesh_id"`
		}
		_ = json.Unmarshal(data, &extracted)

		args += fmt.Sprintf(" --mesh-id %s", extracted.MeshID)
	}
	return Cmd{
		args: args,
		log: func(out string, err error) {
			if err != nil {
				logger.Error(fmt.Errorf(out), "failed delete", "type", kind, "key", key)
			} else {
				logger.Info("delete", "type", kind, "key", key)
			}
		},
	}
}

func objKey(kind string, data json.RawMessage) string {
	key := kindKey(kind)
	value := gjson.Get(string(data), key)
	if value.Exists() {
		return value.String()
	}
	logger.Error(fmt.Errorf(kind), "no object key", "data", string(data))
	return ""
}

func kindKey(kind string) string {
	if kind == "catalogservice" {
		return "service_id"
	}
	return fmt.Sprintf("%s_key", kind)
}

func kindFlag(kind string) string {
	if kind == "catalogservice" {
		return "service-id"
	}
	return fmt.Sprintf("%s-key", kind)
}
