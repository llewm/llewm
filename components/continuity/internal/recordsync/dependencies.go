package recordsync

import (
	"context"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type dependencyIdentity struct{ kind, id string }
type dependencyRevision struct {
	dependencyIdentity
	revision int
}
type dependencyEdge struct {
	owner    string
	selector portable.ImportDependency
}

// dependencyClosure indexes typed forward/reverse selectors for this validated
// snapshot. It retains unresolved selectors: an arriving target still finds its
// earlier dependents. Exact identities include every competing envelope, while
// any/prior selectors conservatively include all qualifying revisions. Graph
// traversal tolerates cycles; only the canonical importer decides their validity.
// Unknown rules decline the optimization rather than asserting independence.
// Building this graph still visits the snapshot; it is not a durable delta proof.
func dependencyClosure(ctx context.Context, objects, changed []portable.EncodedEnvelope) ([]portable.EncodedEnvelope, bool) {
	return dependencyClosureWithSelectors(ctx, objects, changed, portable.ImportDependencies)
}

func dependencyClosureWithSelectors(ctx context.Context, objects, changed []portable.EncodedEnvelope, dependencies func(portable.Envelope) ([]portable.ImportDependency, bool)) ([]portable.EncodedEnvelope, bool) {
	forward := make(map[string][]portable.ImportDependency)
	reverse := make(map[dependencyIdentity][]dependencyEdge)
	exactReverse := make(map[dependencyRevision][]string)
	edges := 0
	seen := make(map[string]bool)
	for _, object := range objects {
		if ctx.Err() != nil {
			return nil, false
		}
		e := object.Envelope
		if seen[e.EnvelopeSHA256] {
			continue
		}
		selectors, ok := dependencies(e)
		if !ok {
			return nil, false
		}
		edges += len(selectors)
		if edges > MaxScanEntries {
			return nil, false
		}
		seen[e.EnvelopeSHA256] = true
		forward[e.EnvelopeSHA256] = selectors
		for _, selector := range selectors {
			key := dependencyIdentity{selector.RecordType, selector.RecordID}
			if selector.Mode == portable.DependencyExact {
				exactKey := dependencyRevision{key, selector.Revision}
				exactReverse[exactKey] = append(exactReverse[exactKey], e.EnvelopeSHA256)
				continue
			}
			reverse[key] = append(reverse[key], dependencyEdge{e.EnvelopeSHA256, selector})
		}
	}
	// Consume range reverse edges once, even when a resource has many revisions
	// and notes. Any-revision selectors sort above every finite prior bound.
	for key, list := range reverse {
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i].selector, list[j].selector
			if a.Mode != b.Mode {
				return b.Mode == portable.DependencyAny
			}
			return a.Revision < b.Revision
		})
		reverse[key] = list
	}
	return traverseDependencies(ctx, objects, changed,
		func(hash string) ([]portable.ImportDependency, bool) { return forward[hash], true },
		func(e portable.Envelope) ([]string, bool) {
			key := dependencyIdentity{e.RecordType, e.RecordID}
			owners := exactReverse[dependencyRevision{key, e.RecordRevision}]
			list := reverse[key]
			for len(list) > 0 && dependencyMatches(list[len(list)-1].selector, e.RecordRevision) {
				owners = append(owners, list[len(list)-1].owner)
				list = list[:len(list)-1]
			}
			reverse[key] = list
			return owners, true
		})
}

// traverseDependencies keeps snapshot identities in memory, but lets the caller
// discover forward/reverse edges only for records reached by the closure.
func traverseDependencies(ctx context.Context, objects, changed []portable.EncodedEnvelope, forward func(string) ([]portable.ImportDependency, bool), reverse func(portable.Envelope) ([]string, bool)) ([]portable.EncodedEnvelope, bool) {
	byHash := make(map[string]portable.EncodedEnvelope, len(objects))
	identities := make(map[dependencyIdentity][]string)
	exactIdentities := make(map[dependencyRevision][]string)
	total := 0
	for _, object := range objects {
		if ctx.Err() != nil {
			return nil, false
		}
		e := object.Envelope
		if _, ok := byHash[e.EnvelopeSHA256]; ok {
			continue
		}
		if len(byHash) >= portable.MaxRecordCount {
			return nil, false
		}
		total += len(object.Bytes)
		if total > portable.MaxRecordsBytes {
			return nil, false
		}
		byHash[e.EnvelopeSHA256] = object
		key := dependencyIdentity{e.RecordType, e.RecordID}
		identities[key] = append(identities[key], e.EnvelopeSHA256)
		exactKey := dependencyRevision{key, e.RecordRevision}
		exactIdentities[exactKey] = append(exactIdentities[exactKey], e.EnvelopeSHA256)
	}
	sortedRanges := make(map[dependencyIdentity]bool)
	selected := make(map[string]bool)
	visitedSelectors := make(map[portable.ImportDependency]bool)
	visitedRevisions := make(map[dependencyRevision]bool)
	var queue []string
	add := func(hash string) {
		if !selected[hash] {
			selected[hash] = true
			queue = append(queue, hash)
		}
	}
	for _, object := range changed {
		hash := object.Envelope.EnvelopeSHA256
		if _, ok := byHash[hash]; !ok {
			return nil, false
		}
		add(hash)
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		if ctx.Err() != nil {
			return nil, false
		}
		hash := queue[cursor]
		e := byHash[hash].Envelope
		key := dependencyIdentity{e.RecordType, e.RecordID}
		// Forked versions of one exact identity cannot be separated by closure.
		exactKey := dependencyRevision{key, e.RecordRevision}
		if !visitedRevisions[exactKey] {
			visitedRevisions[exactKey] = true
			for _, other := range exactIdentities[exactKey] {
				add(other)
			}
			owners, ok := reverse(e)
			if !ok {
				return nil, false
			}
			for _, owner := range owners {
				if _, present := byHash[owner]; present {
					add(owner)
				}
			}
		}
		selectors, ok := forward(hash)
		if !ok {
			return nil, false
		}
		for _, selector := range selectors {
			if visitedSelectors[selector] {
				continue
			}
			visitedSelectors[selector] = true
			if selector.Mode == portable.DependencyExact {
				for _, target := range exactIdentities[dependencyRevision{dependencyIdentity{selector.RecordType, selector.RecordID}, selector.Revision}] {
					add(target)
				}
				continue
			}
			targetKey := dependencyIdentity{selector.RecordType, selector.RecordID}
			list := identities[targetKey]
			// Prior/any selectors select nested prefixes. Sort on first use
			// and consume each target once across all bounds for this identity.
			// Exact-only identities need no range sorting.
			if !sortedRanges[targetKey] {
				sort.Slice(list, func(i, j int) bool {
					return byHash[list[i]].Envelope.RecordRevision < byHash[list[j]].Envelope.RecordRevision
				})
				sortedRanges[targetKey] = true
			}
			for len(list) > 0 && dependencyMatches(selector, byHash[list[0]].Envelope.RecordRevision) {
				add(list[0])
				list = list[1:]
			}
			identities[targetKey] = list
		}
	}
	sort.Strings(queue)
	result := make([]portable.EncodedEnvelope, 0, len(queue))
	for _, hash := range queue {
		result = append(result, byHash[hash])
	}
	return result, true
}

func dependencyMatches(selector portable.ImportDependency, revision int) bool {
	switch selector.Mode {
	case portable.DependencyExact:
		return revision == selector.Revision
	case portable.DependencyAny:
		return true
	case portable.DependencyPrior:
		return revision < selector.Revision
	default:
		return false
	}
}
