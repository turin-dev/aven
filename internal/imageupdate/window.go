package imageupdate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/cronx"
)

// Enabled reports whether automatic updates are on ("" is the stored default, disabled).
func Enabled(policy string) bool { return policy == "patch" || policy == "minor" }

// ValidateWindow checks the custom UTC maintenance schedule stored with an instance.
func ValidateWindow(window string, days []int, startHour, endHour int) error {
	if window == "" {
		window = "anytime"
	}
	switch window {
	case "anytime", "weekends", "night":
		return nil
	case "custom":
		if len(days) == 0 {
			return fmt.Errorf("custom maintenance window needs at least one weekday")
		}
		if startHour < 0 || startHour > 23 || endHour < 1 || endHour > 24 || startHour >= endHour {
			return fmt.Errorf("custom maintenance hours must be an increasing range within 0..24")
		}
		seen := map[int]bool{}
		for _, d := range days {
			if d < 0 || d > 6 || seen[d] {
				return fmt.Errorf("custom maintenance weekdays must be distinct values from 0..6")
			}
			seen[d] = true
		}
		return nil
	default:
		return fmt.Errorf("image_update_window must be anytime, weekends, night or custom")
	}
}

// InWindow evaluates maintenance periods in UTC. Cron matching keeps the schedule
// behavior aligned with the other scheduled work in Aven.
func InWindow(window string, days []int, startHour, endHour int, at time.Time) bool {
	if window == "" {
		window = "anytime"
	}
	var expr string
	switch window {
	case "anytime":
		expr = "* * * * *"
	case "weekends":
		expr = "* * * * 0,6"
	case "night":
		expr = "* 2-5 * * *"
	case "custom":
		if ValidateWindow(window, days, startHour, endHour) != nil {
			return false
		}
		dayList := append([]int(nil), days...)
		sort.Ints(dayList)
		dayParts := make([]string, len(dayList))
		for i, d := range dayList {
			dayParts[i] = fmt.Sprint(d)
		}
		hours := fmt.Sprint(startHour)
		if endHour-startHour > 1 {
			hours += "-" + fmt.Sprint(endHour-1)
		}
		expr = "* " + hours + " * * " + strings.Join(dayParts, ",")
	default:
		return false
	}
	s, err := cronx.Parse(expr)
	return err == nil && s.Match(at.UTC())
}
