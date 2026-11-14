package ingest

import (
	"strings"
	"time"
)

// ClockRule is one user's statement that, during a period, the clock of one of their cameras (make
// and model) showed a fixed UTC offset, which nothing in its files reveals: a Panasonic left on
// summer time all year, an Insta360 on UTC+1 through a summer. It comes from the user's row in
// camera_clock_rules, so it describes that user's camera and never another user's of the same model.
//
// A rule is matched on the file's EXIF Make and Model, without case and surrounding space, and on its
// wall clock, never on UTC: the rule is what turns the wall clock into UTC.
type ClockRule struct {
	ID        string
	Make      string
	Model     string
	OffsetMin int        // minutes east of UTC the camera's clock showed
	StartsOn  *time.Time // midnight UTC of the first day (a date on the camera's clock); nil: no start
	EndsOn    *time.Time // midnight UTC of the last day, which is included; nil: no end
}

// ClockRules are the rules of one owner. The zero value holds none.
type ClockRules []ClockRule

// Match returns the rule for this camera whose period holds wall, a clock reading held as a time in
// UTC like wallClock's: StartsOn <= wall < EndsOn + 1 day. A file without a make or a model matches
// nothing. The database allows one rule per camera and day; should two match anyway, the first wins.
func (rs ClockRules) Match(cameraMake, model string, wall time.Time) (ClockRule, bool) {
	cameraMake, model = normalCamera(cameraMake), normalCamera(model)
	if cameraMake == "" || model == "" {
		return ClockRule{}, false
	}
	day := calendarDay(wall)
	for _, rule := range rs {
		if normalCamera(rule.Make) != cameraMake || normalCamera(rule.Model) != model {
			continue
		}
		if rule.StartsOn != nil && day.Before(calendarDay(*rule.StartsOn)) {
			continue
		}
		if rule.EndsOn != nil && day.After(calendarDay(*rule.EndsOn)) {
			continue
		}
		return rule, true
	}
	return ClockRule{}, false
}

// normalCamera is how a rule's make and model are compared with a file's. CleanTag comes first
// because a camera's stored make and model went through it, and a rule is made from those; then case
// goes, as for the make rules.
func normalCamera(s string) string {
	return strings.ToLower(CleanTag(s))
}

// calendarDay is t's date in t's own location, as midnight UTC. For a wall clock held as UTC that is
// the camera's date; for a rule's day it is that day whatever location it was read in.
func calendarDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
