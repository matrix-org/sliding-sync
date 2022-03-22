package internal

import (
	"fmt"
	"strings"
)

// Metadata about a room that is consistent between all users in the room.
type RoomMetadata struct {
	RoomID               string
	Heroes               []Hero
	NameEvent            string // the content of m.room.name, NOT the calculated name
	CanonicalAlias       string
	JoinCount            int
	InviteCount          int
	LastMessageTimestamp uint64
	Encrypted            bool
	Tombstoned           bool
}

func (m *RoomMetadata) RemoveHero(userID string) {
	for i, h := range m.Heroes {
		if h.ID == userID {
			m.Heroes = append(m.Heroes[0:i], m.Heroes[i+1:]...)
			return
		}
	}
}

type Hero struct {
	ID   string
	Name string
}

func CalculateRoomName(heroInfo *RoomMetadata, maxNumNamesPerRoom int) string {
	// If the room has an m.room.name state event with a non-empty name field, use the name given by that field.
	if heroInfo.NameEvent != "" {
		return heroInfo.NameEvent
	}
	// If the room has an m.room.canonical_alias state event with a valid alias field, use the alias given by that field as the name.
	if heroInfo.CanonicalAlias != "" {
		return heroInfo.CanonicalAlias
	}
	// If none of the above conditions are met, a name should be composed based on the members of the room.
	disambiguatedNames := disambiguate(heroInfo.Heroes)
	totalNumOtherUsers := int(heroInfo.JoinCount + heroInfo.InviteCount - 1)
	isAlone := totalNumOtherUsers <= 0

	// If m.joined_member_count + m.invited_member_count is less than or equal to 1 (indicating the member is alone),
	// the client should use the rules BELOW to indicate that the room was empty. For example, "Empty Room (was Alice)",
	// "Empty Room (was Alice and 1234 others)", or "Empty Room" if there are no heroes.
	if len(heroInfo.Heroes) == 0 && isAlone {
		return "Empty Room"
	}

	// If the number of m.heroes for the room are greater or equal to m.joined_member_count + m.invited_member_count - 1,
	// then use the membership events for the heroes to calculate display names for the users (disambiguating them if required)
	// and concatenating them.
	if len(heroInfo.Heroes) >= totalNumOtherUsers {
		if len(disambiguatedNames) == 1 {
			return disambiguatedNames[0]
		}
		calculatedRoomName := strings.Join(disambiguatedNames[:len(disambiguatedNames)-1], ", ") + " and " + disambiguatedNames[len(disambiguatedNames)-1]
		if isAlone {
			return fmt.Sprintf("Empty Room (was %s)", calculatedRoomName)
		}
		return calculatedRoomName
	}

	// if we're here then len(heroes) < (joinedCount + invitedCount - 1)
	numEntries := len(disambiguatedNames)
	if numEntries > maxNumNamesPerRoom {
		numEntries = maxNumNamesPerRoom
	}
	calculatedRoomName := fmt.Sprintf(
		"%s and %d others", strings.Join(disambiguatedNames[:numEntries], ", "), totalNumOtherUsers-numEntries,
	)

	// If there are fewer heroes than m.joined_member_count + m.invited_member_count - 1,
	// and m.joined_member_count + m.invited_member_count is greater than 1, the client should use the heroes to calculate
	// display names for the users (disambiguating them if required) and concatenating them alongside a count of the remaining users.
	if (heroInfo.JoinCount + heroInfo.InviteCount) > 1 {
		return calculatedRoomName
	}

	// If m.joined_member_count + m.invited_member_count is less than or equal to 1 (indicating the member is alone),
	// the client should use the rules above to indicate that the room was empty. For example, "Empty Room (was Alice)",
	// "Empty Room (was Alice and 1234 others)", or "Empty Room" if there are no heroes.
	return fmt.Sprintf("Empty Room (was %s)", calculatedRoomName)
}

func disambiguate(heroes []Hero) []string {
	displayNames := make(map[string][]int)
	for i, h := range heroes {
		name := h.Name
		if name == "" {
			name = h.ID
		}
		displayNames[name] = append(displayNames[name], i)
	}
	disambiguatedNames := make([]string, len(heroes))
	for name, indexes := range displayNames {
		if len(indexes) == 1 {
			disambiguatedNames[indexes[0]] = name
			continue
		}
		// disambiguate all these heroes
		for _, i := range indexes {
			h := heroes[i]
			name := h.Name
			if name == "" {
				name = h.ID
			}
			disambiguatedNames[i] = fmt.Sprintf("%s (%s)", name, h.ID)
		}
	}
	return disambiguatedNames
}
