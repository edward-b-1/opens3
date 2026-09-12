package notify

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Rule is one Queue/Topic/CloudFunction configuration of a bucket.
type Rule struct {
	ID     string
	ARN    string
	Events []string
	Prefix string
	Suffix string
	// HasPrefix/HasSuffix record that a rule was present (an empty
	// prefix value is legal and matches everything).
	HasPrefix bool
	HasSuffix bool
}

// Configuration is a parsed NotificationConfiguration.
type Configuration struct {
	Rules []Rule
	// EventBridge records an EventBridgeConfiguration element (accepted,
	// not delivered anywhere).
	EventBridge bool
}

type xmlFilterRule struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

type xmlTarget struct {
	ID     string   `xml:"Id"`
	Topic  string   `xml:"Topic"`
	Queue  string   `xml:"Queue"`
	Lambda string   `xml:"CloudFunction"`
	Events []string `xml:"Event"`
	Filter *struct {
		S3Key struct {
			Rules []xmlFilterRule `xml:"FilterRule"`
		} `xml:"S3Key"`
	} `xml:"Filter"`
}

type xmlConfiguration struct {
	XMLName     xml.Name    `xml:"NotificationConfiguration"`
	Topics      []xmlTarget `xml:"TopicConfiguration"`
	Queues      []xmlTarget `xml:"QueueConfiguration"`
	Lambdas     []xmlTarget `xml:"CloudFunctionConfiguration"`
	EventBridge *struct{}   `xml:"EventBridgeConfiguration"`
}

// ParseConfiguration parses NotificationConfiguration XML. Empty input
// yields an empty configuration. Rules are returned in document order
// (topics, queues, cloud functions).
func ParseConfiguration(raw []byte) (*Configuration, error) {
	cfg := &Configuration{}
	if len(raw) == 0 {
		return cfg, nil
	}
	var x xmlConfiguration
	if err := xml.Unmarshal(raw, &x); err != nil {
		return nil, fmt.Errorf("notify: parse configuration: %w", err)
	}
	cfg.EventBridge = x.EventBridge != nil
	for _, list := range [][]xmlTarget{x.Topics, x.Queues, x.Lambdas} {
		for _, t := range list {
			r := Rule{ID: t.ID, ARN: t.Topic + t.Queue + t.Lambda, Events: t.Events}
			if t.Filter != nil {
				for _, fr := range t.Filter.S3Key.Rules {
					switch strings.ToLower(fr.Name) {
					case "prefix":
						if r.HasPrefix {
							return nil, fmt.Errorf("notify: duplicate filter rule %q", fr.Name)
						}
						r.Prefix, r.HasPrefix = fr.Value, true
					case "suffix":
						if r.HasSuffix {
							return nil, fmt.Errorf("notify: duplicate filter rule %q", fr.Name)
						}
						r.Suffix, r.HasSuffix = fr.Value, true
					default:
						return nil, fmt.Errorf("notify: unsupported filter rule name %q", fr.Name)
					}
				}
			}
			cfg.Rules = append(cfg.Rules, r)
		}
	}
	return cfg, nil
}

// Validate checks a configuration: every rule needs at least one valid
// event name and an ARN that resolve accepts. Filter-rule constraints
// (only prefix/suffix, at most one of each) are enforced by
// ParseConfiguration.
func Validate(cfg *Configuration, resolve func(arn string) error) error {
	for i, r := range cfg.Rules {
		if len(r.Events) == 0 {
			return fmt.Errorf("notify: configuration %d has no events", i)
		}
		for _, e := range r.Events {
			if !ValidEventName(e) {
				return fmt.Errorf("notify: event %q is not supported", e)
			}
		}
		if r.ARN == "" {
			return fmt.Errorf("notify: configuration %d has no destination", i)
		}
		if resolve != nil {
			if err := resolve(r.ARN); err != nil {
				return fmt.Errorf("notify: destination %s: %w", r.ARN, err)
			}
		}
	}
	return nil
}

// EventNames lists the event names S3 accepts in a configuration.
var EventNames = []string{
	"s3:ObjectCreated:*", "s3:ObjectCreated:Put", "s3:ObjectCreated:Post", "s3:ObjectCreated:Copy", "s3:ObjectCreated:CompleteMultipartUpload",
	"s3:ObjectRemoved:*", "s3:ObjectRemoved:Delete", "s3:ObjectRemoved:DeleteMarkerCreated",
	"s3:ObjectRestore:*", "s3:ObjectRestore:Post", "s3:ObjectRestore:Completed", "s3:ObjectRestore:Delete",
	"s3:ReducedRedundancyLostObject",
	"s3:Replication:*", "s3:Replication:OperationFailedReplication", "s3:Replication:OperationMissedThreshold", "s3:Replication:OperationReplicatedAfterThreshold", "s3:Replication:OperationNotTracked",
	"s3:LifecycleExpiration:*", "s3:LifecycleExpiration:Delete", "s3:LifecycleExpiration:DeleteMarkerCreated", "s3:LifecycleTransition", "s3:IntelligentTiering",
	"s3:ObjectTagging:*", "s3:ObjectTagging:Put", "s3:ObjectTagging:Delete", "s3:ObjectAcl:Put", "s3:ObjectRetention:Put",
	"s3:ObjectAnnotation:*", "s3:ObjectAnnotation:Put", "s3:ObjectAnnotation:Delete",
}

// ValidEventName reports whether e is an accepted event name.
func ValidEventName(e string) bool {
	for _, n := range EventNames {
		if n == e {
			return true
		}
	}
	return false
}

// EventMatches reports whether pattern (e.g. "s3:ObjectCreated:*" or
// "s3:ObjectCreated:Put") covers the concrete event name. The "s3:"
// prefix is optional on both sides.
func EventMatches(pattern, name string) bool {
	pattern = strings.TrimPrefix(pattern, "s3:")
	name = strings.TrimPrefix(name, "s3:")
	if pattern == name {
		return true
	}
	if strings.HasSuffix(pattern, ":*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == "*"
}

// Match returns the rules whose events and key filters match. Prefix and
// suffix values may contain MinIO-style wildcards (* and ?).
func (c *Configuration) Match(eventName, key string) []Rule {
	var out []Rule
	for _, r := range c.Rules {
		if r.matches(eventName, key) {
			out = append(out, r)
		}
	}
	return out
}

func (r Rule) matches(eventName, key string) bool {
	ok := false
	for _, e := range r.Events {
		if EventMatches(e, eventName) {
			ok = true
			break
		}
	}
	if !ok {
		return false
	}
	if r.HasPrefix && !filterMatch(r.Prefix, key, true) {
		return false
	}
	if r.HasSuffix && !filterMatch(r.Suffix, key, false) {
		return false
	}
	return true
}

func filterMatch(value, key string, prefix bool) bool {
	if !strings.ContainsAny(value, "*?") {
		if prefix {
			return strings.HasPrefix(key, value)
		}
		return strings.HasSuffix(key, value)
	}
	if prefix {
		return wildMatch(value+"*", key)
	}
	return wildMatch("*"+value, key)
}

// wildMatch matches s against pattern with * (any run) and ? (one char).
func wildMatch(pattern, s string) bool {
	p, i := 0, 0
	starP, starI := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			starP, starI = p, i
			p++
		case starP >= 0:
			p = starP + 1
			starI++
			i = starI
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
