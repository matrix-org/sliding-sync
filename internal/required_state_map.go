package internal

const StateKeyLazy = "$LAZY"

type RequiredStateMap struct {
	eventTypesWithWildcardStateKeys map[string]struct{}
	stateKeysForWildcardEventType   []string
	eventTypeToStateKeys            map[string][]string
	allState                        bool
	lazyLoading                     bool
}

func NewRequiredStateMap(eventTypesWithWildcardStateKeys map[string]struct{},
	stateKeysForWildcardEventType []string,
	eventTypeToStateKeys map[string][]string,
	allState, lazyLoading bool) *RequiredStateMap {
	return &RequiredStateMap{
		eventTypesWithWildcardStateKeys: eventTypesWithWildcardStateKeys,
		stateKeysForWildcardEventType:   stateKeysForWildcardEventType,
		eventTypeToStateKeys:            eventTypeToStateKeys,
		allState:                        allState,
		lazyLoading:                     lazyLoading,
	}
}

func (rsm *RequiredStateMap) IsLazyLoading() bool {
	return rsm.lazyLoading
}

func (rsm *RequiredStateMap) Include(evType, stateKey string) bool {
	if rsm.allState {
		// "additional entries FILTER OUT the returned set of state events. These additional entries cannot use '*' themselves."
		includedStateKeys := rsm.eventTypeToStateKeys[evType]
		if len(includedStateKeys) > 0 {
			for _, sk := range includedStateKeys {
				if sk == stateKey {
					return true
				}
			}
			return false
		}
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

func (rsm *RequiredStateMap) Empty() bool {
	return !rsm.allState && !rsm.lazyLoading &&
		len(rsm.eventTypeToStateKeys) == 0 &&
		len(rsm.stateKeysForWildcardEventType) == 0 &&
		len(rsm.eventTypesWithWildcardStateKeys) == 0
}

// work out what to ask the storage layer: if we have wildcard event types we need to pull all
// room state and cannot only pull out certain event types. If we have wildcard state keys we
// need to use an empty list for state keys.
func (rsm *RequiredStateMap) QueryStateMap() map[string][]string {
	queryStateMap := make(map[string][]string)
	if rsm.allState {
		return queryStateMap
	}
	if len(rsm.stateKeysForWildcardEventType) == 0 { // no wildcard event types
		for evType, stateKeys := range rsm.eventTypeToStateKeys {
			if evType == "m.room.member" && rsm.lazyLoading {
				queryStateMap[evType] = nil
			} else {
				queryStateMap[evType] = stateKeys
			}
		}
		for evType := range rsm.eventTypesWithWildcardStateKeys {
			queryStateMap[evType] = nil
		}
	}
	return queryStateMap
}
