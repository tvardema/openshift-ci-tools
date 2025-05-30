/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package approvers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/prow/pkg/layeredsets"
	"sigs.k8s.io/prow/pkg/plugins/ownersconfig"
)

const (
	// ApprovalNotificationName defines the name used in the title for the approval notifications.
	ApprovalNotificationName = "ApprovalNotifier"
)

// Repo allows querying and interacting with OWNERS information in a repo.
type Repo interface {
	Approvers(path string) layeredsets.String
	LeafApprovers(path string) sets.Set[string]
	FindApproverOwnersForFile(file string) string
	IsNoParentOwners(path string) bool
	IsAutoApproveUnownedSubfolders(directory string) bool
	Filenames() ownersconfig.Filenames
}

// Owners provides functionality related to owners of a specific code change.
type Owners struct {
	// filenamesUnfiltered contains all files in a given PR, including those
	// that do not need approval because of IsAutoApproveUnownedSubfolders
	filenamesUnfiltered []string
	// filenames refers to the files in a given PR, not to OWNERS files. Files
	// that are a directory below an Owners file with IsAutoApproveUnownedSubfolders
	// are excluded here but kept in filenamesUnfiltered.
	filenames []string
	repo      Repo
	seed      int64

	log *logrus.Entry
}

// NewOwners consturcts a new Owners instance. filenames is the slice of files changed.
func NewOwners(log *logrus.Entry, filenames []string, r Repo, s int64) Owners {
	return Owners{filenamesUnfiltered: filenames, filenames: filenames, repo: r, seed: s, log: log}
}

// GetApprovers returns a map from ownersFiles -> people that are approvers in them
func (o Owners) GetApprovers() map[string]sets.Set[string] {
	ownersToApprovers := map[string]sets.Set[string]{}
	for _, toApprove := range o.filenames {
		ownersFile := o.repo.FindApproverOwnersForFile(toApprove)
		approvers := o.repo.Approvers(toApprove).Set()
		if _, ok := ownersToApprovers[ownersFile]; !ok {
			ownersToApprovers[ownersFile] = sets.New[string]()
		}
		ownersToApprovers[ownersFile] = ownersToApprovers[ownersFile].Union(approvers)
	}

	owners := o.GetOwnersSet()
	for k := range ownersToApprovers {
		if !owners.Has(k) {
			delete(ownersToApprovers, k)
		}
	}

	return ownersToApprovers
}

// GetLeafApprovers returns a map from ownersFiles -> people that are approvers in them (only the leaf)
func (o Owners) GetLeafApprovers() map[string]sets.Set[string] {
	ownersToApprovers := map[string]sets.Set[string]{}

	for _, toApprove := range o.filenames {
		ownersFile := o.repo.FindApproverOwnersForFile(toApprove)
		approvers := o.repo.LeafApprovers(toApprove)
		if _, ok := ownersToApprovers[ownersFile]; !ok {
			ownersToApprovers[ownersFile] = sets.New[string]()
		}
		ownersToApprovers[ownersFile] = ownersToApprovers[ownersFile].Union(approvers)
	}

	owners := o.GetOwnersSet()
	for k := range ownersToApprovers {
		if !owners.Has(k) {
			delete(ownersToApprovers, k)
		}
	}

	return ownersToApprovers
}

// GetAllPotentialApprovers returns the people from relevant owners files needed to get the PR approved
func (o Owners) GetAllPotentialApprovers() []string {
	approversOnly := []string{}
	for _, approverList := range o.GetLeafApprovers() {
		for approver := range approverList {
			approversOnly = append(approversOnly, approver)
		}
	}
	sort.Strings(approversOnly)
	if len(approversOnly) == 0 {
		o.log.Debug("No potential approvers exist. Does the repo have OWNERS files?")
	}
	return approversOnly
}

// GetReverseMap returns a map from people -> OWNERS files for which they are an approver
func (o Owners) GetReverseMap(approvers map[string]sets.Set[string]) map[string]sets.Set[string] {
	approverOwnersfiles := map[string]sets.Set[string]{}
	for ownersFile, approvers := range approvers {
		for approver := range approvers {
			if _, ok := approverOwnersfiles[approver]; ok {
				approverOwnersfiles[approver].Insert(ownersFile)
			} else {
				approverOwnersfiles[approver] = sets.New[string](ownersFile)
			}
		}
	}
	return approverOwnersfiles
}

func findMostCoveringApprover(allApprovers []string, coveredApproversSet sets.Set[string], reverseMap map[string]sets.Set[string], unapproved sets.Set[string]) string {
	maxCovered := 0
	var bestPerson string
	for _, approver := range allApprovers {
		filesCanApprove := reverseMap[approver]
		if filesCanApprove.Intersection(unapproved).Len() > maxCovered && !coveredApproversSet.Has(approver) {
			maxCovered = len(filesCanApprove)
			bestPerson = approver
		}
	}

	// todo: make it better.

	return bestPerson
}

// temporaryUnapprovedFiles returns the list of files that wouldn't be
// approved by the given set of approvers.
func (o Owners) temporaryUnapprovedFiles(approvers sets.Set[string]) sets.Set[string] {
	ap := NewApprovers(o)
	for approver := range approvers {
		ap.AddApprover(approver, "", false)
	}
	return ap.UnapprovedFiles()
}

// KeepCoveringApprovers finds who we should keep as suggested approvers given a pre-selection
// knownApprovers must be a subset of potentialApprovers.
func (o Owners) KeepCoveringApprovers(reverseMap map[string]sets.Set[string], knownApprovers sets.Set[string], potentialApprovers []string) sets.Set[string] {
	if len(potentialApprovers) == 0 {
		o.log.Debug("No potential approvers exist to filter for relevance. Does this repo have OWNERS files?")
	}
	keptApprovers := sets.New[string]()

	unapproved := o.temporaryUnapprovedFiles(knownApprovers)

	for _, suggestedApprover := range sets.List(o.GetSuggestedApprovers(reverseMap, potentialApprovers)) {
		if reverseMap[suggestedApprover].Intersection(unapproved).Len() != 0 {
			keptApprovers.Insert(suggestedApprover)
		}
	}

	return keptApprovers
}

// GetSuggestedApprovers solves the exact cover problem, finding an approver capable of
// approving every OWNERS file in the PR
func (o Owners) GetSuggestedApprovers(reverseMap map[string]sets.Set[string], potentialApprovers []string) sets.Set[string] {
	ap := NewApprovers(o)
	for !ap.RequirementsMet() {
		newApprover := findMostCoveringApprover(potentialApprovers, ap.GetCurrentApproversSet(), reverseMap, ap.UnapprovedFiles())
		if newApprover == "" {
			o.log.Debugf("Couldn't find/suggest approvers for each files. Unapproved: %q", sets.List(ap.UnapprovedFiles()))
			return ap.GetCurrentApproversSet()
		}
		ap.AddApprover(newApprover, "", false)
	}

	return ap.GetCurrentApproversSet()
}

// GetOwnersSet returns a set containing all the Owners files necessary to get the PR approved
func (o Owners) GetOwnersSet() sets.Set[string] {
	owners := sets.New[string]()

	var newFilenames []string
	for _, toApprove := range o.filenames {
		ownersFile := o.repo.FindApproverOwnersForFile(toApprove)
		// If the ownersfile for toApprove is in the parent folder and has IsAutoApproveUnownedSubfolders enabled, we purge
		// the file from our filenames list, because it doesn't need approval
		if strings.Contains(filepath.Dir(filepath.Dir(toApprove)), ownersFile) && o.repo.IsAutoApproveUnownedSubfolders(ownersFile) {
			continue
		}
		owners.Insert(o.repo.FindApproverOwnersForFile(toApprove))
		newFilenames = append(newFilenames, toApprove)

	}
	o.filenames = newFilenames
	o.removeSubdirs(owners)
	return owners
}

// GetShuffledApprovers shuffles the potential approvers so that we don't
// always suggest the same people.
func (o Owners) GetShuffledApprovers() []string {
	approversList := o.GetAllPotentialApprovers()
	order := rand.New(rand.NewSource(o.seed)).Perm(len(approversList))
	people := make([]string, 0, len(approversList))
	for _, i := range order {
		people = append(people, approversList[i])
	}
	return people
}

// removeSubdirs takes a set of directories as an input and removes all subdirectories.
// E.g. [a, a/b/c, d/e, d/e/f] -> [a, d/e]
// Subdirs will not be removed if they are configured to have no parent OWNERS files or if any
// OWNERS file in the relative path between the subdir and the higher level dir is configured to
// have no parent OWNERS files.
func (o Owners) removeSubdirs(dirs sets.Set[string]) {
	canonicalize := func(p string) string {
		if p == "." {
			return ""
		}
		return p
	}
	for _, dir := range sets.List(dirs) {
		path := dir
		for {
			if o.repo.IsNoParentOwners(path) || canonicalize(path) == "" {
				break
			}
			path = filepath.Dir(path)
			if dirs.Has(canonicalize(path)) {
				dirs.Delete(dir)
				break
			}
		}
	}
}

// Approval has the information about each approval on a PR
type Approval struct {
	Login     string // Login of the approver (can include uppercase)
	How       string // How did the approver approved
	Reference string // Where did the approver approved
	NoIssue   bool   // Approval also accepts missing associated issue
}

// String creates a link for the approval. Use `Login` if you just want the name.
func (a Approval) String() string {
	return fmt.Sprintf(
		`*<a href="%s" title="%s">%s</a>*`,
		a.Reference,
		a.How,
		a.Login,
	)
}

// Approvers is struct that provide functionality with regard to approvals of a specific
// code change.
type Approvers struct {
	owners          Owners
	approvers       map[string]Approval // The keys of this map are normalized to lowercase.
	assignees       sets.Set[string]
	AssociatedIssue int
	RequireIssue    bool

	ManuallyApproved func() bool
}

// CaseInsensitiveIntersection runs the intersection between to sets.Set[string] in a
// case-insensitive way. It returns the lowercased intersection.
func CaseInsensitiveIntersection(one, other sets.Set[string]) sets.Set[string] {
	lower := sets.New[string]()
	for item := range other {
		lower.Insert(strings.ToLower(item))
	}

	intersection := sets.New[string]()
	for item := range one {
		if lower.Has(strings.ToLower(item)) {
			intersection.Insert(item)
		}
	}
	return intersection
}

// NewApprovers create a new "Approvers" with no approval.
func NewApprovers(owners Owners) Approvers {
	return Approvers{
		owners:    owners,
		approvers: map[string]Approval{},
		assignees: sets.New[string](),

		ManuallyApproved: func() bool {
			return false
		},
	}
}

// shouldNotOverrideApproval decides whether or not we should keep the
// original approval:
// If someone approves a PR multiple times, we only want to keep the
// latest approval, unless a previous approval was "no-issue", and the
// most recent isn't.
func (ap *Approvers) shouldNotOverrideApproval(login string, noIssue bool) bool {
	login = strings.ToLower(login)
	approval, alreadyApproved := ap.approvers[login]

	return alreadyApproved && approval.NoIssue && !noIssue
}

// AddLGTMer adds a new LGTM Approver
func (ap *Approvers) AddLGTMer(login, reference string, noIssue bool) {
	if ap.shouldNotOverrideApproval(login, noIssue) {
		return
	}
	ap.approvers[strings.ToLower(login)] = Approval{
		Login:     login,
		How:       "LGTM",
		Reference: reference,
		NoIssue:   noIssue,
	}
}

// AddApprover adds a new Approver
func (ap *Approvers) AddApprover(login, reference string, noIssue bool) {
	if ap.shouldNotOverrideApproval(login, noIssue) {
		return
	}
	ap.approvers[strings.ToLower(login)] = Approval{
		Login:     login,
		How:       "Approved",
		Reference: reference,
		NoIssue:   noIssue,
	}
}

// AddAuthorSelfApprover adds the author self approval
func (ap *Approvers) AddAuthorSelfApprover(login, reference string, noIssue bool) {
	if ap.shouldNotOverrideApproval(login, noIssue) {
		return
	}
	ap.approvers[strings.ToLower(login)] = Approval{
		Login:     login,
		How:       "Author self-approved",
		Reference: reference,
		NoIssue:   noIssue,
	}
}

// RemoveApprover removes an approver from the list.
func (ap *Approvers) RemoveApprover(login string) {
	delete(ap.approvers, strings.ToLower(login))
}

// AddAssignees adds assignees to the list
func (ap *Approvers) AddAssignees(logins ...string) {
	for _, login := range logins {
		ap.assignees.Insert(strings.ToLower(login))
	}
}

// GetCurrentApproversSet returns the set of approvers (login only, normalized to lower case)
func (ap Approvers) GetCurrentApproversSet() sets.Set[string] {
	currentApprovers := sets.New[string]()

	for approver := range ap.approvers {
		currentApprovers.Insert(approver)
	}

	return currentApprovers
}

// GetCurrentApproversSetCased returns the set of approvers logins with the original cases.
func (ap Approvers) GetCurrentApproversSetCased() sets.Set[string] {
	currentApprovers := sets.New[string]()

	for _, approval := range ap.approvers {
		currentApprovers.Insert(approval.Login)
	}

	return currentApprovers
}

// GetNoIssueApproversSet returns the set of "no-issue" approvers (login
// only)
func (ap Approvers) GetNoIssueApproversSet() sets.Set[string] {
	approvers := sets.New[string]()

	for approver := range ap.NoIssueApprovers() {
		approvers.Insert(approver)
	}

	return approvers
}

// GetFilesApprovers returns a map from files -> list of current approvers.
func (ap Approvers) GetFilesApprovers() map[string]sets.Set[string] {
	filesApprovers := map[string]sets.Set[string]{}
	currentApprovers := ap.GetCurrentApproversSetCased()
	for ownersFilename, potentialApprovers := range ap.owners.GetApprovers() {
		// The order of parameter matters here:
		// - currentApprovers is the list of github handles that have approved
		// - potentialApprovers is the list of handles in the OWNER
		// files (lower case).
		//
		// We want to keep the syntax of the github handle
		// rather than the potential mis-cased username found in
		// the OWNERS file, that's why it's the first parameter.
		filesApprovers[ownersFilename] = CaseInsensitiveIntersection(currentApprovers, potentialApprovers)
	}

	return filesApprovers
}

// NoIssueApprovers returns the list of people who have "no-issue"
// approved the pull-request. They are included in the list if they can
// approve one of the files.
func (ap Approvers) NoIssueApprovers() map[string]Approval {
	nia := map[string]Approval{}
	reverseMap := ap.owners.GetReverseMap(ap.owners.GetApprovers())

	for login, approver := range ap.approvers {
		if !approver.NoIssue {
			continue
		}

		if len(reverseMap[login]) == 0 {
			continue
		}

		nia[login] = approver
	}

	return nia
}

// UnapprovedFiles returns owners files that still need approval
func (ap Approvers) UnapprovedFiles() sets.Set[string] {
	unapproved := sets.New[string]()
	ownersSet := ap.owners.GetOwnersSet()
	currentApprovers := ap.GetCurrentApproversSetCased()

	for _, toApprove := range ap.owners.filenames {
		ownersFile := ap.owners.repo.FindApproverOwnersForFile(toApprove)
		if !ownersSet.Has(ownersFile) {
			continue
		}

		if CaseInsensitiveIntersection(ap.owners.repo.Approvers(toApprove).Set(), currentApprovers).Len() == 0 {
			unapproved.Insert(ownersFile)
		}
	}
	return unapproved
}

// GetFiles returns owners files that still need approval.
func (ap Approvers) GetFiles(baseURL *url.URL, branch string) []File {
	var allOwnersFiles []File
	filesApprovers := ap.GetFilesApprovers()
	unapproverdFiles := ap.UnapprovedFiles()
	for _, file := range sets.List(ap.owners.GetOwnersSet()) {
		if unapproverdFiles.Has(file) {
			allOwnersFiles = append(allOwnersFiles, UnapprovedFile{
				baseURL:        baseURL,
				filepath:       file,
				ownersFilename: ap.owners.repo.Filenames().Owners,
				approvers:      filesApprovers[file],
				branch:         branch,
			})
		} else {
			allOwnersFiles = append(allOwnersFiles, ApprovedFile{
				baseURL:        baseURL,
				filepath:       file,
				ownersFilename: ap.owners.repo.Filenames().Owners,
				approvers:      filesApprovers[file],
				branch:         branch,
			})
		}
	}

	return allOwnersFiles
}

// GetCCs gets the list of suggested approvers for a pull-request.  It
// now considers current assignees as potential approvers. Here is how
// it works:
// - We find suggested approvers from all potential approvers, but
// remove those that are not useful considering current approvers and
// assignees. This only uses leaf approvers to find the closest
// approvers to the changes.
// - We find a subset of suggested approvers from current
// approvers, suggested approvers and assignees, but we remove those
// that are not useful considering suggested approvers and current
// approvers. This uses the full approvers list, and will result in root
// approvers to be suggested when they are assigned.
// We return the union of the two sets: suggested and suggested
// assignees.
// The goal of this second step is to only keep the assignees that are
// the most useful.
func (ap Approvers) GetCCs() []string {
	randomizedApprovers := ap.owners.GetShuffledApprovers()

	currentApprovers := ap.GetCurrentApproversSet()
	approversAndAssignees := currentApprovers.Union(ap.assignees)
	leafReverseMap := ap.owners.GetReverseMap(ap.owners.GetLeafApprovers())
	suggested := ap.owners.KeepCoveringApprovers(leafReverseMap, approversAndAssignees, randomizedApprovers)
	approversAndSuggested := currentApprovers.Union(suggested)
	everyone := approversAndSuggested.Union(ap.assignees)
	fullReverseMap := ap.owners.GetReverseMap(ap.owners.GetApprovers())
	keepAssignees := ap.owners.KeepCoveringApprovers(fullReverseMap, approversAndSuggested, sets.List(everyone))

	return sets.List(suggested.Union(keepAssignees))
}

// AreFilesApproved returns a bool indicating whether or not OWNERS files associated with
// the PR are approved.  A PR with no OWNERS files is not considered approved. If this
// returns true, the PR may still not be fully approved depending on the associated issue
// requirement
func (ap Approvers) AreFilesApproved() bool {
	return (len(ap.owners.filenames) != 0 || len(ap.owners.filenamesUnfiltered) != 0) && ap.UnapprovedFiles().Len() == 0
}

// RequirementsMet returns a bool indicating whether the PR has met all approval requirements:
// - all OWNERS files associated with the PR have been approved AND
// EITHER
//   - the munger config is such that an issue is not required to be associated with the PR
//   - that there is an associated issue with the PR
//   - an OWNER has indicated that the PR is trivial enough that an issue need not be associated with the PR
func (ap Approvers) RequirementsMet() bool {
	return ap.AreFilesApproved() && (!ap.RequireIssue || ap.AssociatedIssue != 0 || len(ap.NoIssueApprovers()) != 0)
}

// IsApproved returns a bool indicating whether the PR is fully approved.
// If a human manually added the approved label, this returns true, ignoring normal approval rules.
func (ap Approvers) IsApproved() bool {
	return ap.RequirementsMet() || ap.ManuallyApproved()
}

// ListApprovals returns the list of approvals
func (ap Approvers) ListApprovals() []Approval {
	approvals := []Approval{}

	for _, approver := range sets.List(ap.GetCurrentApproversSet()) {
		approvals = append(approvals, ap.approvers[approver])
	}

	return approvals
}

// ListNoIssueApprovals returns the list of "no-issue" approvals
func (ap Approvers) ListNoIssueApprovals() []Approval {
	approvals := []Approval{}

	for _, approver := range sets.List(ap.GetNoIssueApproversSet()) {
		approvals = append(approvals, ap.approvers[approver])
	}

	return approvals
}

// AssignedCCs returns potential approvers that are already assigned
func (ap Approvers) AssignedCCs() []string {
	return sets.List(sets.New[string](ap.GetCCs()...).Intersection(ap.assignees))
}

// SuggestedCCs returns potential approvers that are not already assigned
func (ap Approvers) SuggestedCCs() []string {
	return sets.List(sets.New[string](ap.GetCCs()...).Difference(ap.assignees))
}

// File in an interface for files
type File interface {
	String() string
}

// ApprovedFile contains the information of a an approved file.
type ApprovedFile struct {
	baseURL        *url.URL
	filepath       string
	ownersFilename string
	// approvers is the set of users that approved this file change.
	approvers sets.Set[string]
	branch    string
}

// UnapprovedFile contains the information of a an unapproved file.
type UnapprovedFile struct {
	baseURL        *url.URL
	filepath       string
	ownersFilename string
	// approvers is the set of users that partially approved this file change.
	approvers sets.Set[string]
	branch    string
}

func (a ApprovedFile) String() string {
	fullOwnersPath := filepath.Join(a.filepath, a.ownersFilename)
	if strings.HasSuffix(a.filepath, ".md") {
		fullOwnersPath = a.filepath
	}
	link := fmt.Sprintf("%s/blob/%s/%v",
		a.baseURL.String(),
		a.branch,
		fullOwnersPath,
	)
	return fmt.Sprintf("- ~~[%s](%s)~~ [%v]\n", fullOwnersPath, link, strings.Join(sets.List(a.approvers), ","))
}

func (ua UnapprovedFile) String() string {
	fullOwnersPath := filepath.Join(ua.filepath, ua.ownersFilename)
	if strings.HasSuffix(ua.filepath, ".md") {
		fullOwnersPath = ua.filepath
	}
	link := fmt.Sprintf("%s/blob/%s/%v",
		ua.baseURL.String(),
		ua.branch,
		fullOwnersPath,
	)
	if ua.approvers.Len() > 0 {
		return fmt.Sprintf("- **[%s](%s)** [%v]\n  > Need more approvers for rest parts.\n", fullOwnersPath, link, strings.Join(sets.List(ua.approvers), ","))
	}
	return fmt.Sprintf("- **[%s](%s)**\n", fullOwnersPath, link)
}

// GenerateTemplate takes a template, name and data, and generates
// the corresponding string.
func GenerateTemplate(templ, name string, data interface{}) (string, error) {
	buf := bytes.NewBufferString("")
	if messageTempl, err := template.New(name).Parse(templ); err != nil {
		return "", fmt.Errorf("failed to parse template for %s: %w", name, err)
	} else if err := messageTempl.Execute(buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template for %s: %w", name, err)
	}
	return buf.String(), nil
}

// GetMessage returns the comment body that we want the approve plugin to display on PRs
// The comment shows:
//   - a list of approvers files (and links) needed to get the PR approved
//   - a list of approvers files with strikethroughs that already have an approver's approval
//   - a suggested list of people from each OWNERS files that can fully approve the PR
//   - how an approver can indicate their approval
//   - how an approver can cancel their approval
func GetMessage(ap Approvers, linkURL *url.URL, commandHelpLink, prProcessLink, org, repo, branch string) *string {
	linkURL.Path = org + "/" + repo
	message, err := GenerateTemplate(`{{if (and (not .ap.RequirementsMet) (call .ap.ManuallyApproved )) }}
Approval requirements bypassed by manually added approval.

{{end -}}
This pull-request has been approved by:{{range $index, $approval := .ap.ListApprovals}}{{if $index}}, {{else}} {{end}}{{$approval}}{{end}}

{{- if (and (not .ap.AreFilesApproved) (not (call .ap.ManuallyApproved))) }}
{{ if len .ap.SuggestedCCs -}}
{{- if len .ap.AssignedCCs -}}
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from {{range $index, $cc := .ap.AssignedCCs}}{{if $index}}, {{end}}{{printf "[%s](https://github.com/%s)" $cc $cc}}{{end}} and additionally assign {{range $index, $cc := .ap.SuggestedCCs}}{{if $index}}, {{end}}{{printf "[%s](https://github.com/%s)" $cc $cc}}{{end}} for approval. For more information see [the Code Review Process]({{ .prProcessLink }}).
{{- else -}}
**Once this PR has been reviewed and has the lgtm label**, please assign {{range $index, $cc := .ap.SuggestedCCs}}{{if $index}}, {{end}}{{printf "[%s](https://github.com/%s)" $cc $cc}}{{end}} for approval. For more information see [the Code Review Process]({{ .prProcessLink }}).
{{- end}}
{{- else -}}
{{- if len .ap.AssignedCCs -}}
**Once this PR has been reviewed and has the lgtm label**, please ask for approval from {{range $index, $cc := .ap.AssignedCCs}}{{if $index}}, {{end}}{{printf "[%s](https://github.com/%s)" $cc $cc}}{{end}}. For more information see [the Code Review Process]({{ .prProcessLink }}).
{{- end}}
{{- end}}
{{- end}}

{{if not .ap.RequireIssue -}}
{{else if .ap.AssociatedIssue -}}
Associated issue: *#{{.ap.AssociatedIssue}}*

{{ else if len .ap.NoIssueApprovers -}}
Associated issue requirement bypassed by:{{range $index, $approval := .ap.ListNoIssueApprovals}}{{if $index}}, {{else}} {{end}}{{$approval}}{{end}}

{{ else if call .ap.ManuallyApproved -}}
*No associated issue*. Requirement bypassed by manually added approval.

{{ else -}}
*No associated issue*. Update pull-request body to add a reference to an issue, or get approval with `+"`/approve no-issue`"+`

{{ end -}}

The full list of commands accepted by this bot can be found [here]({{ .commandHelpLink }}?repo={{ .org }}%2F{{ .repo }}).

{{ if (or .ap.AreFilesApproved (call .ap.ManuallyApproved)) -}}
The pull request process is described [here]({{ .prProcessLink }})

{{ end -}}
<details {{if (and (not .ap.AreFilesApproved) (not (call .ap.ManuallyApproved))) }}open{{end}}>
Needs approval from an approver in each of these files:

{{range .ap.GetFiles .baseURL .branch}}{{.}}{{end}}
Approvers can indicate their approval by writing `+"`/approve`"+` in a comment
Approvers can cancel approval by writing `+"`/approve cancel`"+` in a comment
</details>`, "message", map[string]interface{}{"ap": ap, "baseURL": linkURL, "commandHelpLink": commandHelpLink, "prProcessLink": prProcessLink, "org": org, "repo": repo, "branch": branch})
	if err != nil {
		ap.owners.log.WithError(err).Errorf("Error generating message.")
		return nil
	}
	message += getGubernatorMetadata(ap.GetCCs())

	title, err := GenerateTemplate("This PR is **{{if not .IsApproved}}NOT {{end}}APPROVED**", "title", ap)
	if err != nil {
		ap.owners.log.WithError(err).Errorf("Error generating title.")
		return nil
	}

	return notification(ApprovalNotificationName, title, message)
}

func notification(name, arguments, context string) *string {
	str := "[" + strings.ToUpper(name) + "]"

	args := strings.TrimSpace(arguments)
	if args != "" {
		str += " " + args
	}

	ctx := strings.TrimSpace(context)
	if ctx != "" {
		str += "\n\n" + ctx
	}

	return &str
}

// getGubernatorMetadata returns a JSON string with machine-readable information about approvers.
// This MUST be kept in sync with gubernator/github/classifier.py, particularly get_approvers.
func getGubernatorMetadata(toBeAssigned []string) string {
	bytes, err := json.Marshal(map[string][]string{"approvers": toBeAssigned})
	if err == nil {
		return fmt.Sprintf("\n<!-- META=%s -->", bytes)
	}
	return ""
}
