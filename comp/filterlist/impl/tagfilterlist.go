// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package filterlistimpl

import (
	"slices"

	"github.com/twmb/murmur3"

	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	filterlist "github.com/DataDog/datadog-agent/comp/filterlist/def"
	"github.com/DataDog/datadog-agent/pkg/metrics/metricname"
	"github.com/DataDog/datadog-agent/pkg/metrics/tagname"
)

// TagMatcher manages removing tags from metrics with a given name.
type tagMatcher struct {
	MetricTags map[string]hashedMetricTagList
}

// MetricTagList is for loading the data from the configuration.
type MetricTagList struct {
	Tags   []string `yaml:"tags"`
	Action string   `yaml:"action"`
}

// MetricTagListEntry is for loading the new list-based configuration format.
type MetricTagListEntry struct {
	MetricName string   `mapstructure:"metric_name" yaml:"metric_name" json:"metric_name"`
	Action     string   `mapstructure:"action" yaml:"action" json:"action"`
	Tags       []string `mapstructure:"tags" yaml:"tags" json:"tags"`
}

type action bool

const (
	exclude action = true
	include action = false
)

// hashedMetricTagList contains the list of tags hashed using murmur3.
type hashedMetricTagList struct {
	tags   []uint64
	action action
}

func newHashedMetricTagList(action action, tags []uint64) hashedMetricTagList {
	// The tags must be sorted as we do a binary search to test membership.
	slices.Sort(tags)
	tags = slices.Compact(tags)

	return hashedMetricTagList{
		action: action,
		tags:   tags,
	}
}

func NewEmptyTagMatcher() filterlist.TagMatcher {
	return tagMatcher{
		MetricTags: map[string]hashedMetricTagList{},
	}
}

// NewTagMatcher creates a TagMatcher from a map of metric names to tag configuration.
func NewTagMatcher(metrics map[string]MetricTagList, log log.Component) filterlist.TagMatcher {
	return newTagMatcher(metrics, log)
}

func newTagMatcher(metrics map[string]MetricTagList, log log.Component) tagMatcher {
	entries := make([]MetricTagListEntry, 0, len(metrics))
	for name, list := range metrics {
		entries = append(entries, MetricTagListEntry{MetricName: name, Action: list.Action, Tags: list.Tags})
	}
	return compileTagFilterList(entries, log)
}

// compileTagFilterList compiles tag filter list entries, from any source, into
// a tagMatcher. Metric names and tag keys are normalized the way the intake
// stores them, and entries naming the same metric are then merged; see
// pkg/metrics/tagname/SPEC.md §10. The result does not depend on entry order.
//
// As in the intake, metric names keep their case and tag keys are lowercased:
// `My-Metric` compiles to `My_Metric`, and `Env` to `env`.
func compileTagFilterList(entries []MetricTagListEntry, log log.Component) tagMatcher {
	type rule struct {
		action action
		keys   []string
		// For warnings only: the first configured name for this metric, and
		// whether any of its entries listed tags.
		firstName string
		hadTags   bool
	}
	rules := make(map[string]*rule, len(entries))

	var nameBuf [metricname.MaxLength]byte
	for _, entry := range entries {
		if entry.MetricName == "" {
			log.Warn("skipping metric_tag_filterlist entry with empty metric_name")
			continue
		}

		act := parseAction(entry, log)

		normalized, ok := metricname.NormalizeAppend(nameBuf[:0], entry.MetricName)
		if !ok {
			log.Warnf("skipping metric_tag_filterlist entry for metric %q: the Datadog intake rejects this metric name", entry.MetricName)
			continue
		}
		name := string(normalized)

		keys := make([]string, 0, len(entry.Tags))
		for _, tag := range entry.Tags {
			key, ok := tagname.RuleKey(tag)
			if !ok {
				log.Warnf("ignoring tag %q in metric_tag_filterlist entry for metric %q: tag keys cannot contain ':'; list the tag key only, without a value", tag, entry.MetricName)
				continue
			}
			keys = append(keys, key)
		}

		current, exists := rules[name]
		if !exists {
			rules[name] = &rule{action: act, keys: keys, firstName: entry.MetricName, hadTags: len(entry.Tags) > 0}
			continue
		}
		current.hadTags = current.hadTags || len(entry.Tags) > 0
		switch {
		case current.action == act:
			current.keys = append(current.keys, keys...)
		default:
			log.Warnf("metric_tag_filterlist entries %q and %q are for the same metric and configure both include and exclude; keeping only the exclude entries", current.firstName, entry.MetricName)
			if act == exclude {
				current.action, current.keys = exclude, keys
			}
		}
	}

	for _, r := range rules {
		if r.action == include && len(r.keys) == 0 && r.hadTags {
			log.Warnf("metric_tag_filterlist include entry for metric %q has no usable tag keys left, so it removes every tag", r.firstName)
		}
	}

	hashed := make(map[string]hashedMetricTagList, len(rules))
	for name, r := range rules {
		hashed[name] = newHashedMetricTagList(r.action, hashTags(r.keys))
	}

	return tagMatcher{
		MetricTags: hashed,
	}
}

// parseAction returns the action of a tag filter list entry. An empty action
// means exclude.
func parseAction(entry MetricTagListEntry, log log.Component) action {
	switch entry.Action {
	case "include":
		return include
	case "exclude", "":
		return exclude
	default:
		log.Warnf("`metric_tag_filterlist.%s.action` configuration value %q should be either `include` or `exclude`. Defaulting to `exclude`.", entry.MetricName, entry.Action)
		return exclude
	}
}

func hashTags(tags []string) []uint64 {
	hashed := make([]uint64, 0, len(tags))
	for _, tag := range tags {
		hashed = append(hashed, murmur3.StringSum64(tag))
	}

	return hashed
}

// ShouldStripTags returns true if it has been configured to strip tags
// from the given metric name. The returned function reports whether a tag
// should be kept. Names and tag keys are compared in the form the intake
// stores them in, so a rule for `my_metric` and `env` applies to a sample
// named `my-metric` with the tag `Env:prod`.
func (m tagMatcher) ShouldStripTags(metricName string) (func(tag string) bool, bool) {
	// The common case: no rules, so nothing to normalize.
	if len(m.MetricTags) == 0 {
		return nil, false
	}

	tm, ok := m.lookup(metricName)
	if !ok {
		return nil, false
	}

	keepTag := func(tag string) bool {
		// Key allocates only for tags the intake would rewrite.
		hashedTag := murmur3.StringSum64(tagname.Key(tag))
		_, found := slices.BinarySearch(tm.tags, hashedTag)
		return found != bool(tm.action)
	}

	return keepTag, true
}

// lookup returns the rule for the normalized form of metricName. Names are
// almost always already normalized, and are then looked up without a copy.
func (m tagMatcher) lookup(metricName string) (hashedMetricTagList, bool) {
	if metricname.IsNormalized(metricName) {
		tm, ok := m.MetricTags[metricName]
		return tm, ok
	}
	var nameBuf [metricname.MaxLength]byte
	name, ok := metricname.NormalizeAppend(nameBuf[:0], metricName)
	if !ok {
		return hashedMetricTagList{}, false
	}
	// Indexing with a converted []byte does not allocate.
	tm, ok := m.MetricTags[string(name)]
	return tm, ok
}
