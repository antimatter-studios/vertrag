package fuzz

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Accept lists the statuses a generated request may be answered with without
// that counting as a finding.
//
// The case for it: a probe judges a server against its description, and a
// description does not carry business rules. A generated body can satisfy every
// schema constraint and still be refused for a reason the document never states
// — an account with insufficient funds, a market that is closed, an idempotency
// key already used. vertrag reports that as "the server returned 422 for a body
// its own schema permits, so it disagrees with its description", which is
// literally true and useless: the disagreement is real, it is in the
// description, and the team already knows.
//
// The case against, which is why this is built the way it is: a list of
// statuses that make findings disappear is a machine for hiding bugs. Set
// `accept: [400, 422]` and a genuine "this endpoint rejects everything" is
// indistinguishable from a clean run.
//
// So acceptance is not silence. Every excused answer is counted, and the run
// reports the count. A suite that quietly stopped testing anything shows up as
// a suppression count that matches its probe count, which is a number somebody
// will eventually look at — where a hidden finding is a number nobody can see.
type Accept []int

// Excuses reports whether a status is one the caller said not to report.
//
// The range check is here rather than only in CheckAccept, and deliberately
// duplicates it. CheckAccept is a startup validation and startup validations
// get bypassed — by a caller constructing Options directly, by a future config
// path that forgets to call it, by a test. This is the one finding that must
// never be lost to a configuration mistake, so the runtime refuses it on its
// own account and does not rely on having been checked.
func (a Accept) Excuses(code int) bool {
	if code < 400 || code >= 500 {
		return false
	}
	for _, want := range a {
		if want == code {
			return true
		}
	}
	return false
}

// CheckAccept refuses an acceptance list that would hide a server failure.
//
// 5xx is never acceptable and cannot be made so. A 500 does not mean the server
// refused the input, it means it broke on it, and that is the one thing a probe
// exists to find — see judge, which reports it whatever mode the request was
// drawn for. Allowing it here would let one config line turn the tool off while
// leaving it looking like it was on.
//
// Anything below 400 is refused for the opposite reason: those are not
// rejections, so excusing one means excusing a request that SUCCEEDED. For an
// invalid body that is precisely the finding — the constraints are not enforced
// — and for a valid one there was nothing to excuse.
func CheckAccept(accept Accept) error {
	var serverErrors, notRejections []string
	for _, code := range accept {
		switch {
		case code >= 500:
			serverErrors = append(serverErrors, strconv.Itoa(code))
		case code < 400:
			notRejections = append(notRejections, strconv.Itoa(code))
		}
	}

	if len(serverErrors) > 0 {
		return fmt.Errorf(
			"fuzz.accept lists %s, and a 5xx cannot be accepted: it means the server broke on the "+
				"request rather than refusing it, which is the finding a probing phase exists to "+
				"produce. Fix the endpoint, or take the operation out of the run with `skip`",
			strings.Join(serverErrors, ", "))
	}
	if len(notRejections) > 0 {
		return fmt.Errorf(
			"fuzz.accept lists %s, and only a rejection can be accepted: a status under 400 means "+
				"the request was answered, so for a body the schema forbids it is the finding that "+
				"the constraints are not enforced, and for one the schema permits there is nothing "+
				"to excuse", strings.Join(notRejections, ", "))
	}
	return nil
}

// Suppression counts what Accept excused.
//
// It is a pointer on Options and threaded through the probe loop rather than
// derived afterwards, because by the time a probe returns, an excused answer is
// indistinguishable from one that never happened — both are "no finding". The
// count has to be taken at the moment the decision is made or it cannot be
// taken at all.
type Suppression struct {
	ByStatus map[int]int
	Total    int
}

// Record notes one excused answer.
func (s *Suppression) Record(code int) {
	if s == nil {
		return
	}
	if s.ByStatus == nil {
		s.ByStatus = map[int]int{}
	}
	s.ByStatus[code]++
	s.Total++
}

// Describe renders the counts for the run summary, most frequent first so the
// status doing the most hiding is the one read first.
func (s *Suppression) Describe() string {
	if s == nil || s.Total == 0 {
		return ""
	}
	codes := make([]int, 0, len(s.ByStatus))
	for code := range s.ByStatus {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool {
		if s.ByStatus[codes[i]] != s.ByStatus[codes[j]] {
			return s.ByStatus[codes[i]] > s.ByStatus[codes[j]]
		}
		return codes[i] < codes[j]
	})

	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", s.ByStatus[code], code))
	}
	return strings.Join(parts, ", ")
}
