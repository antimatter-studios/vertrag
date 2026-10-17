package reporter

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// Surface is how much of an API description a run reached.
//
// Every other report lists the transactions that ran, and says nothing about
// the ones a filter, a skip, a hook or a failed dependency kept from running —
// so a suite that tested half an API reads exactly as cleanly as one that
// tested all of it. This is the other half of the account: of everything the
// description defines, what did the run reach, and why not the rest.
//
// An operation is reached when a request to it got an answer, whatever the
// answer was. A documented status is reached when the operation answered with
// it. Neither says the answer was right; the transactions say that, and a run
// can reach every operation and still fail every one of them.
type Surface struct {
	// Phases are each phase's own figures, in the order the run ran them.
	Phases []SurfaceFigures `json:"phases"`

	// Run is what the run reached in any phase, and is what the gate reads.
	Run SurfaceFigures `json:"run"`

	// Missed is every operation no phase reached, and every documented status
	// of a reached operation that no phase reached, in description order and
	// each with the reason.
	Missed []SurfaceMiss `json:"missed"`

	// Excluded are the operations left out of the figures, each with the
	// reason it was given and where.
	Excluded []SurfaceExclusion `json:"excluded"`

	// Required is the percentage of operations the run had to reach. Zero
	// means nothing was required.
	Required float64 `json:"required,omitempty"`

	// DryRun is set when nothing was sent. Run then counts the operations the
	// run WOULD have sent a request to, which is the most it could reach, and
	// no status can be counted at all.
	DryRun bool `json:"dryRun,omitempty"`
}

// SurfaceFigures are one phase's reach, or the run's.
//
// The totals leave out excluded operations, and the statuses they document,
// rather than counting them as reached: an exclusion says an operation does
// not need reaching, not that it was, and a figure that quietly folded the two
// together would read higher than anything the run did.
type SurfaceFigures struct {
	Phase             string `json:"phase"`
	Operations        int    `json:"operations"`
	OperationsReached int    `json:"operationsReached"`
	Statuses          int    `json:"statuses"`
	StatusesReached   int    `json:"statusesReached"`
}

// OperationShare is the percentage of operations reached.
func (f SurfaceFigures) OperationShare() float64 { return share(f.OperationsReached, f.Operations) }

// StatusShare is the percentage of documented statuses reached.
func (f SurfaceFigures) StatusShare() float64 { return share(f.StatusesReached, f.Statuses) }

// share is a percentage. Nothing to reach is all of it reached: a description
// whose every operation is excluded has nothing left a run could miss.
func share(reached, total int) float64 {
	if total == 0 {
		return 100
	}
	return 100 * float64(reached) / float64(total)
}

// Percent writes a share to one decimal place, rounded DOWN.
//
// Rounded to nearest, 1999 of 2000 operations prints as 100.0% — beside a gate
// of 100 that has just failed, which is a report contradicting itself in the
// one line a reader came to it for. A share is only ever shown as full when it
// is.
func Percent(value float64) string {
	return fmt.Sprintf("%.1f%%", math.Floor(value*10)/10)
}

// SurfaceMiss is one thing the run did not reach, and why.
type SurfaceMiss struct {
	// Operation is named the way the rest of the surface report names it:
	// the method and path template, or a GraphQL field's transaction name.
	Operation string `json:"operation"`

	// Status is the documented status that was not reached. Empty when the
	// miss is the whole operation.
	Status string `json:"status,omitempty"`

	Reason string `json:"reason"`
}

// SurfaceExclusion is an operation left out of the figures.
type SurfaceExclusion struct {
	Operation string `json:"operation"`
	Reason    string `json:"reason"`

	// Source is where the exclusion was written: the configuration file's
	// `surface.exclude`, or the operation's own `x-vertrag-exclude`.
	Source string `json:"source"`

	// Reached is set when the run reached the operation anyway. An exclusion
	// only decides what counts, so nothing stops that — see Note.
	Reached bool `json:"reached,omitempty"`
}

// Note is what a report adds after an exclusion the run reached anyway.
//
// It is worded for the reader it could cost the most. An exclusion written as
// "charges a real card" reads as though it kept the request from going out,
// and it did not: what keeps a request from going out is `skip`. Saying so
// where the request is shown to have been sent is cheaper than the reader
// finding out from their card statement — and for an exclusion that is merely
// stale, the same sentence says it can go.
func (e SurfaceExclusion) Note() string {
	if !e.Reached {
		return ""
	}
	return "reached anyway: an exclusion decides what counts, not what is sent, and `skip` is what keeps a request back"
}

// Met reports whether the run reached what it was required to.
func (s Surface) Met() bool {
	return s.Required == 0 || s.Run.OperationShare() >= s.Required
}

// Verdict is the sentence that ends every rendering of the surface: what the
// run reached against what it was required to, or nothing when nothing was
// required. Worded once, here, so that the terminal and a published report
// cannot disagree about whether the gate held.
func (s Surface) Verdict() string {
	if s.Required == 0 {
		return ""
	}
	reached := fmt.Sprintf("%d of %d operation(s), %s", s.Run.OperationsReached, s.Run.Operations,
		Percent(s.Run.OperationShare()))
	required := Percent(s.Required)
	switch {
	case s.DryRun && s.Met():
		return fmt.Sprintf("A run would send a request to %s, so the %s it requires is within reach.", reached, required)
	case s.DryRun:
		return fmt.Sprintf("A run would send a request to %s, so it cannot reach the %s it requires.", reached, required)
	case s.Met():
		return fmt.Sprintf("The run reached %s, which meets the %s it requires.", reached, required)
	default:
		return fmt.Sprintf("The run reached %s, below the %s it requires.", reached, required)
	}
}

// StatusMisses are the documented statuses not reached, gathered per operation
// and reason so that one operation whose four error statuses were all answered
// 200 reads as one line rather than four.
func (s Surface) StatusMisses() []SurfaceStatusMisses {
	var out []SurfaceStatusMisses
	index := map[[2]string]int{}
	for _, miss := range s.Missed {
		if miss.Status == "" {
			continue
		}
		key := [2]string{miss.Operation, miss.Reason}
		if at, seen := index[key]; seen {
			out[at].Statuses = append(out[at].Statuses, miss.Status)
			continue
		}
		index[key] = len(out)
		out = append(out, SurfaceStatusMisses{Operation: miss.Operation, Statuses: []string{miss.Status}, Reason: miss.Reason})
	}
	return out
}

// OperationMisses are the operations no phase reached.
func (s Surface) OperationMisses() []SurfaceMiss {
	var out []SurfaceMiss
	for _, miss := range s.Missed {
		if miss.Status == "" {
			out = append(out, miss)
		}
	}
	return out
}

// SurfaceStatusMisses is one operation's unreached statuses that share a reason.
type SurfaceStatusMisses struct {
	Operation string
	Statuses  []string
	Reason    string
}

// StatusList is the statuses as one cell of text.
func (m SurfaceStatusMisses) StatusList() string { return strings.Join(m.Statuses, ", ") }

// Rows are the figures as a report's table shows them: each phase, then the
// run. A run of one phase has nothing for the run's row to add, so it is left
// out rather than repeating the line above it.
func (s Surface) Rows() []SurfaceRow {
	var rows []SurfaceRow
	for _, figures := range s.Phases {
		rows = append(rows, s.row(figures.Phase, figures))
	}
	if len(s.Phases) != 1 {
		label := "the run"
		if s.DryRun {
			label = "would send"
		}
		rows = append(rows, s.row(label, s.Run))
	}
	return rows
}

func (s Surface) row(label string, figures SurfaceFigures) SurfaceRow {
	row := SurfaceRow{
		Label: label,
		Operations: fmt.Sprintf("%d of %d  %s", figures.OperationsReached, figures.Operations,
			Percent(figures.OperationShare())),
	}
	if !s.DryRun {
		row.Statuses = fmt.Sprintf("%d of %d  %s", figures.StatusesReached, figures.Statuses,
			Percent(figures.StatusShare()))
	}
	return row
}

// SurfaceRow is one line of the figures table, already the text it shows.
type SurfaceRow struct {
	Label      string
	Operations string
	// Statuses is empty for a dry run, which received no status to count.
	Statuses string
}

// WithSurface hands the surface figures to every reporter with somewhere to put
// them.
//
// The reporters are built before the run, so a file that cannot be opened stops
// it before the first request; the figures exist only after the last phase. So
// they are handed over here, between the two, rather than at construction.
func WithSurface(r Reporter, surface *Surface) Reporter {
	switch r := r.(type) {
	case Multi:
		out := make(Multi, len(r))
		for i, each := range r {
			out[i] = WithSurface(each, surface)
		}
		return out
	case JUnit:
		r.Surface = surface
		return r
	case Markdown:
		r.Surface = surface
		return r
	case HTML:
		r.Surface = surface
		return r
	case JSON:
		r.Surface = surface
		return r
	}
	return r
}

// WriteSurface writes the surface section a run ends with on the terminal.
//
// It is printed on every run, clean or not, which is the opposite of the rule
// the other end-of-run sections follow — and for the opposite reason. They
// report a defect, and a line saying no defect was found is one readers learn
// to skip. This reports how much was tested, and a run that tested less than
// its reader assumes is exactly the run that has nothing else to say.
func WriteSurface(w io.Writer, s Surface, color bool) {
	heading := "API surface reached"
	if s.DryRun {
		heading = "API surface a run would reach (dry run: nothing was sent)"
	}
	fmt.Fprintf(w, "\n%s\n\n", heading)

	rows := s.Rows()
	label, operations := len("the run"), len("operations")
	for _, row := range rows {
		label = max(label, len(row.Label))
		operations = max(operations, len(row.Operations))
	}
	if s.DryRun {
		fmt.Fprintf(w, "  %-*s  %s\n", label, "", "operations")
	} else {
		fmt.Fprintf(w, "  %-*s  %-*s   %s\n", label, "", operations, "operations", "documented statuses")
	}
	for _, row := range rows {
		if s.DryRun {
			fmt.Fprintf(w, "  %-*s  %s\n", label, row.Label, row.Operations)
			continue
		}
		fmt.Fprintf(w, "  %-*s  %-*s   %s\n", label, row.Label, operations, row.Operations, row.Statuses)
	}

	if missed := s.OperationMisses(); len(missed) > 0 {
		if s.DryRun {
			fmt.Fprint(w, "\n  Operations a run would send nothing to:\n")
		} else {
			fmt.Fprint(w, "\n  Operations no phase reached:\n")
		}
		width := 0
		for _, miss := range missed {
			width = max(width, len(miss.Operation))
		}
		for _, miss := range missed {
			fmt.Fprintf(w, "    %-*s  %s\n", width, miss.Operation, miss.Reason)
		}
	}

	if missed := s.StatusMisses(); len(missed) > 0 {
		fmt.Fprint(w, "\n  Documented statuses of reached operations that no phase reached:\n")
		width, statuses := 0, 0
		for _, miss := range missed {
			width = max(width, len(miss.Operation))
			statuses = max(statuses, len(miss.StatusList()))
		}
		for _, miss := range missed {
			fmt.Fprintf(w, "    %-*s  %-*s  %s\n", width, miss.Operation, statuses, miss.StatusList(), miss.Reason)
		}
	}

	if len(s.Excluded) > 0 {
		fmt.Fprint(w, "\n  Excluded, and not counted:\n")
		width := 0
		for _, excluded := range s.Excluded {
			width = max(width, len(excluded.Operation))
		}
		for _, excluded := range s.Excluded {
			fmt.Fprintf(w, "    %-*s  %s (%s)\n", width, excluded.Operation, excluded.Reason, excluded.Source)
			if note := excluded.Note(); note != "" {
				fmt.Fprintf(w, "    %-*s  %s\n", width, "", note)
			}
		}
	}

	if verdict := s.Verdict(); verdict != "" {
		code := green
		if !s.Met() {
			code = red
		}
		fmt.Fprintf(w, "\n  %s\n", paint(color, code, verdict))
	}
}
