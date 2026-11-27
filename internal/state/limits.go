package state

import "time"

// WindowStart is the inclusive start of a limit's window in now's location.
// A week starts on Monday and a month on the first day. Hours and days are
// rolling back from now unless Since names a fixed start.
func (l TokenLimit) WindowStart(now time.Time) (time.Time, bool) {
	loc := now.Location()
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch l.Window {
	case "today":
		return day, true
	case "week":
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset), true
	case "month":
		return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc), true
	case "all":
		return time.Time{}, true
	case "hours":
		if l.Hours <= 0 {
			return time.Time{}, false
		}
		if l.Since != nil {
			return l.Since.In(loc), true
		}
		return local.Add(-time.Duration(l.Hours) * time.Hour), true
	case "days":
		if l.Days <= 0 {
			return time.Time{}, false
		}
		if l.Since != nil {
			return l.Since.In(loc), true
		}
		return local.AddDate(0, 0, -l.Days), true
	default:
		return time.Time{}, false
	}
}

// Matches reports whether a row is in this limit's provider/model scope.
func (l TokenLimit) Matches(rec AttemptRecord) bool {
	if l.Provider != "" && l.Provider != rec.ProviderID {
		return false
	}
	if l.Model != "" && l.Model != rec.Model {
		return false
	}
	return true
}

// Hides reports whether the page should leave this row out. It does not
// affect a token limit.
func (h UsageHide) Hides(rec AttemptRecord) bool {
	return h.ProviderID == rec.ProviderID && h.Model == rec.Model && !rec.Started.After(h.Before)
}

// HiddenOnUsagePage reports whether any configured clear rule hides rec.
func (c Config) HiddenOnUsagePage(rec AttemptRecord) bool {
	for _, hide := range c.UsageHidden {
		if hide.Hides(rec) {
			return true
		}
	}
	return false
}
