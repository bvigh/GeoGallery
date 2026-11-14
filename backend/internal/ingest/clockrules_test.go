package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// day returns midnight UTC of a date, as a rule's StartsOn and EndsOn hold it.
func day(year int, month time.Month, d int) *time.Time {
	t := time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func wallAt(year int, month time.Month, d, hour, minute, second int) time.Time {
	return time.Date(year, month, d, hour, minute, second, 0, time.UTC)
}

func TestClockRules_Match(t *testing.T) {
	tz200 := ClockRule{ID: "tz200", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: day(2019, 1, 1), EndsOn: day(2019, 12, 31)}
	rules := ClockRules{tz200}
	inside := wallAt(2019, 6, 1, 12, 0, 0)

	tests := []struct {
		name        string
		rules       ClockRules
		make, model string
		wall        time.Time
		want        string // the matching rule's ID; empty: none
	}{
		{"the camera inside the period", rules, "Panasonic", "DC-TZ200", inside, "tz200"},
		{"make and model without case and surrounding space", rules, " PANASONIC ", "dc-tz200 ", inside, "tz200"},
		{"text a camera's stored make and model were cleaned of", rules, "Pana\xffsonic\x00", "DC-TZ200\x00", inside, "tz200"},
		{"another model of the make", rules, "Panasonic", "DC-TZ100", inside, ""},
		{"the model under another make", rules, "Leica", "DC-TZ200", inside, ""},
		{"a file without a model", rules, "Panasonic", "", inside, ""},
		{"a file without a make", rules, "", "DC-TZ200", inside, ""},
		{"a model that is only space", rules, "Panasonic", "   ", inside, ""},
		{"no rules", nil, "Panasonic", "DC-TZ200", inside, ""},

		{"the first day from 00:00:00", rules, "Panasonic", "DC-TZ200", wallAt(2019, 1, 1, 0, 0, 0), "tz200"},
		{"the second before the first day", rules, "Panasonic", "DC-TZ200", wallAt(2018, 12, 31, 23, 59, 59), ""},
		{"the last day to 23:59:59", rules, "Panasonic", "DC-TZ200", wallAt(2019, 12, 31, 23, 59, 59), "tz200"},
		{"the last day's last fraction of a second", rules, "Panasonic", "DC-TZ200", wallAt(2019, 12, 31, 23, 59, 59).Add(999 * time.Millisecond), "tz200"},
		{"not the next day's 00:00:00", rules, "Panasonic", "DC-TZ200", wallAt(2020, 1, 1, 0, 0, 0), ""},

		{"no start reaches back to any year",
			ClockRules{{ID: "until", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, EndsOn: day(2019, 12, 31)}},
			"Panasonic", "DC-TZ200", wallAt(1901, 1, 1, 0, 0, 0), "until"},
		{"no start still ends",
			ClockRules{{ID: "until", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, EndsOn: day(2019, 12, 31)}},
			"Panasonic", "DC-TZ200", wallAt(2020, 1, 1, 0, 0, 0), ""},
		{"no end reaches forward to any year",
			ClockRules{{ID: "from", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, StartsOn: day(2019, 1, 1)}},
			"Panasonic", "DC-TZ200", wallAt(2999, 12, 31, 23, 59, 59), "from"},
		{"no end still starts",
			ClockRules{{ID: "from", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, StartsOn: day(2019, 1, 1)}},
			"Panasonic", "DC-TZ200", wallAt(2018, 12, 31, 23, 59, 59), ""},
		{"neither bound is always",
			ClockRules{{ID: "always", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60}},
			"Panasonic", "DC-TZ200", wallAt(1970, 1, 1, 0, 0, 0), "always"},

		{"periods of one camera pick by date, the earlier",
			ClockRules{
				{ID: "winter", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, EndsOn: day(2018, 12, 31)},
				{ID: "summer", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: day(2019, 1, 1)},
			},
			"Panasonic", "DC-TZ200", wallAt(2018, 12, 31, 23, 0, 0), "winter"},
		{"periods of one camera pick by date, the later",
			ClockRules{
				{ID: "winter", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, EndsOn: day(2018, 12, 31)},
				{ID: "summer", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: day(2019, 1, 1)},
			},
			"Panasonic", "DC-TZ200", wallAt(2019, 1, 1, 1, 0, 0), "summer"},
		{"rules of other cameras are passed over",
			ClockRules{
				{ID: "onerS", Make: "Arashi Vision", Model: "Insta360 OneRS", OffsetMin: 60},
				tz200,
			},
			"Panasonic", "DC-TZ200", inside, "tz200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.rules.Match(tt.make, tt.model, tt.wall)
			if tt.want == "" {
				assert.False(t, ok)
				assert.Equal(t, ClockRule{}, got)
				return
			}
			assert.True(t, ok)
			assert.Equal(t, tt.want, got.ID)
		})
	}
}

// A rule's day is its date, whatever location the time was read in: midnight in Warsaw is still the
// day it names, not the evening before in UTC.
func TestClockRules_MatchReadsADayInItsOwnLocation(t *testing.T) {
	ends := time.Date(2019, 12, 31, 0, 0, 0, 0, mustZone(t, "Europe/Warsaw"))
	rules := ClockRules{{ID: "r", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: &ends, EndsOn: &ends}}

	_, ok := rules.Match("Panasonic", "DC-TZ200", wallAt(2019, 12, 31, 23, 30, 0))
	assert.True(t, ok, "the last evening of the day")
	_, ok = rules.Match("Panasonic", "DC-TZ200", wallAt(2019, 12, 30, 23, 30, 0))
	assert.False(t, ok, "not the evening before")
}
