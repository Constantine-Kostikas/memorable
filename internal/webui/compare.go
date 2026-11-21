package webui

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

var errCompareInput = errors.New("invalid version selection")

const (
	compareInputMessage      = "Pick two different versions of this memory."
	compareQuarantineMessage = "This memory is quarantined: its versions stay hidden until it is approved."
)

// compareView is the History tab's comparison of two versions, always old (From) to new (To).
type compareView struct {
	Base            string
	From, To        int
	Options         []compareOption
	Apart           int
	Added, Removed  int
	Summary         string
	PropertyChanges []string
	Diff            []diffLine
	DiffNote        string
	Message         string
	OOB             bool
}

type compareOption struct {
	Version int
	Label   string
	Current bool
}

// defaultCompare picks the previous version against the latest; ok is false with fewer than two versions.
func defaultCompare(history []memory.Version) (from, to int, ok bool) {
	if len(history) < 2 {
		return 0, 0, false
	}
	sorted := sortedVersions(history)
	return sorted[len(sorted)-2].Version, sorted[len(sorted)-1].Version, true
}

// buildCompare diffs two versions of history. A newer from is swapped with to and marked OOB so the
// response also re-renders both selects. Unknown or equal versions return errCompareInput.
func buildCompare(history []memory.Version, current, from, to int) (compareView, error) {
	view := compareView{Options: compareOptions(history, current)}
	if from > to {
		from, to = to, from
		view.OOB = true
	}
	older, okFrom := versionByNumber(history, from)
	newer, okTo := versionByNumber(history, to)
	if !okFrom || !okTo || from == to {
		return view, errCompareInput
	}
	view.From, view.To = from, to
	view.Apart = countBetween(history, from, to)
	view.Diff, view.DiffNote = versionDiff(older, newer)
	for _, line := range view.Diff {
		switch line.Op {
		case "+":
			view.Added++
		case "-":
			view.Removed++
		}
	}
	view.Summary = fmt.Sprintf("%s apart · %s added, %d removed",
		plural(view.Apart, "version"), plural(view.Added, "line"), view.Removed)
	view.PropertyChanges = propertyChanges(older, newer)
	return view, nil
}

func sortedVersions(history []memory.Version) []memory.Version {
	sorted := slices.Clone(history)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	return sorted
}

func compareOptions(history []memory.Version, current int) []compareOption {
	sorted := sortedVersions(history)
	options := make([]compareOption, 0, len(sorted))
	for i := len(sorted) - 1; i >= 0; i-- {
		v := sorted[i]
		label := fmt.Sprintf("v%d · %s", v.Version, newTimeView(v.ChangedAt).Date)
		if v.Version == current {
			label += " (current)"
		}
		options = append(options, compareOption{Version: v.Version, Label: label, Current: v.Version == current})
	}
	return options
}

func versionByNumber(history []memory.Version, number int) (memory.Version, bool) {
	for _, v := range history {
		if v.Version == number {
			return v, true
		}
	}
	return memory.Version{}, false
}

// countBetween counts the recorded versions after from up to and including to.
func countBetween(history []memory.Version, from, to int) int {
	n := 0
	for _, v := range history {
		if v.Version > from && v.Version <= to {
			n++
		}
	}
	return n
}

func propertyChanges(older, newer memory.Version) []string {
	var changes []string
	if older.Type != newer.Type {
		changes = append(changes, fmt.Sprintf("Type: %s to %s", older.Type, newer.Type))
	}
	var tagChanges []string
	for _, tag := range sortedDifference(newer.Tags, older.Tags) {
		tagChanges = append(tagChanges, "+"+tag)
	}
	for _, tag := range sortedDifference(older.Tags, newer.Tags) {
		tagChanges = append(tagChanges, "-"+tag)
	}
	if len(tagChanges) > 0 {
		changes = append(changes, "Tags: "+strings.Join(tagChanges, ", "))
	}
	return changes
}

func sortedDifference(a, b []string) []string {
	var out []string
	for _, item := range a {
		if !slices.Contains(b, item) {
			out = append(out, item)
		}
	}
	slices.Sort(out)
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// handleCompare answers the History tab's version selects with the diff block.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	fetched, err := s.fetchPassive(r, rc, id)
	if errors.Is(err, memory.ErrQuarantined) {
		s.renderStatus(w, http.StatusForbidden, "compare_response", compareView{Message: compareQuarantineMessage})
		return
	}
	if err != nil {
		s.failResponse(w, r, rc, err)
		return
	}
	history, err := rc.Backend.Memories.History(r.Context(), id)
	if err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("load history: %w", err))
		return
	}
	view, err := compareFromQuery(history, fetched.Memory.Version, r)
	view.Base = memoryBase(rc.Name, id)
	if err != nil {
		view.Message = compareInputMessage
		s.renderStatus(w, http.StatusUnprocessableEntity, "compare_response", view)
		return
	}
	s.renderStatus(w, http.StatusOK, "compare_response", view)
}

func compareFromQuery(history []memory.Version, current int, r *http.Request) (compareView, error) {
	query := r.URL.Query()
	from, errFrom := strconv.Atoi(query.Get("from"))
	to, errTo := strconv.Atoi(query.Get("to"))
	if errFrom != nil || errTo != nil {
		return compareView{Options: compareOptions(history, current)}, errCompareInput
	}
	return buildCompare(history, current, from, to)
}
