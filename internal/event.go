package internal

import "github.com/tidwall/gjson"

func IsMembershipChange(eventJSON gjson.Result) bool {
	// membership event possibly, make sure the membership has changed else
	// things like display name changes will count as membership events :(
	prevMembership := "leave"
	pm := eventJSON.Get("unsigned.prev_content.membership")
	if pm.Exists() && pm.Str != "" {
		prevMembership = pm.Str
	}
	currMembership := "leave"
	cm := eventJSON.Get("content.membership")
	if cm.Exists() && cm.Str != "" {
		currMembership = cm.Str
	}
	return prevMembership != currMembership // membership was changed
}
