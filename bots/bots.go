// Package bots содержит список известных чат-ботов, которых не показываем в таблицах.
package bots

import "strings"

var known = map[string]struct{}{
	"nightbot": {}, "streamelements": {}, "streamlabs": {}, "moobot": {},
	"fossabot": {}, "wizebot": {}, "botrix": {}, "sery_bot": {},
	"commanderroot": {}, "soundalerts": {}, "stay_hydrated_bot": {},
	"data_bot": {}, "ohbot": {}, "phantombot": {}, "kickbot": {},
	"pretzelrocks": {}, "creatisbot": {}, "streamholics": {},
	"own3d": {}, "own3dpro": {}, "botisimo": {}, "mixitupbot": {},
}

func Is(login string) bool {
	_, ok := known[strings.ToLower(strings.TrimSpace(login))]

	return ok
}
