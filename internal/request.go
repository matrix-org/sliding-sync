package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

type RequiredStateMap struct {
	eventTypesWithWildcardStateKeys map[string]struct{}
	stateKeysForWildcardEventType   []string
	eventTypeToStateKeys            map[string][]string
	allState                        bool
}

func NewRequiredStateMap(eventTypesWithWildcardStateKeys map[string]struct{},
	stateKeysForWildcardEventType []string,
	eventTypeToStateKeys map[string][]string,
	allState bool) *RequiredStateMap {
	return &RequiredStateMap{
		eventTypesWithWildcardStateKeys: eventTypesWithWildcardStateKeys,
		stateKeysForWildcardEventType:   stateKeysForWildcardEventType,
		eventTypeToStateKeys:            eventTypeToStateKeys,
		allState:                        allState,
	}
}

func (rsm *RequiredStateMap) Include(evType, stateKey string) bool {
	if rsm.allState {
		return true
	}
	// check if we should include this event due to wildcard event types
	for _, sk := range rsm.stateKeysForWildcardEventType {
		if sk == stateKey || sk == "*" {
			return true
		}
	}
	// check if we should include this event due to wildcard state keys
	for et := range rsm.eventTypesWithWildcardStateKeys {
		if et == evType {
			return true
		}
	}
	// check if we should include this event due to exact type/state key match
	for _, sk := range rsm.eventTypeToStateKeys[evType] {
		if sk == stateKey {
			return true
		}
	}
	return false
}

// work out what to ask the storage layer: if we have wildcard event types we need to pull all
// room state and cannot only pull out certain event types. If we have wildcard state keys we
// need to use an empty list for state keys.
func (rsm *RequiredStateMap) QueryStateMap() map[string][]string {
	queryStateMap := make(map[string][]string)
	if len(rsm.stateKeysForWildcardEventType) == 0 { // no wildcard event types
		for evType, stateKeys := range rsm.eventTypeToStateKeys {
			queryStateMap[evType] = stateKeys
		}
		for evType := range rsm.eventTypesWithWildcardStateKeys {
			queryStateMap[evType] = nil
		}
	}
	return queryStateMap
}

func DeviceIDFromRequest(req *http.Request) (string, error) {
	// return a hash of the access token
	ah := req.Header.Get("Authorization")
	if ah == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	accessToken := strings.TrimPrefix(ah, "Bearer ")
	// important that this is a cryptographically secure hash function to prevent
	// preimage attacks where Eve can use a fake token to hash to an existing device ID
	// on the server.
	hash := sha256.New()
	hash.Write([]byte(accessToken))
	return hex.EncodeToString(hash.Sum(nil)), nil
}
