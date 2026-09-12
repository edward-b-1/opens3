package notify

import (
	"fmt"
	"strings"
)

// ARN is a parsed notification destination ARN.
//
// Supported forms:
//
//	arn:opens3:sqs::<name>:<type>                 native (region optional)
//	arn:minio:sqs:<region>:<name>:<type>          MinIO compatible
//	arn:aws:sqs:<region>:<account>:<name>         AWS SQS queue
//	arn:aws:sns:<region>:<account>:<name>         AWS SNS topic
//	arn:aws:lambda:<region>:<account>:function:<name>[:<alias>]
//
// For the AWS forms the resource name is mapped onto the configured
// target of the same name, so bucket configurations can be migrated
// without editing.
type ARN struct {
	Partition string // opens3 | minio | aws…
	Service   string // sqs | sns | lambda
	Region    string
	Name      string // configured target name
	Type      string // webhook, kafka, … (opens3/minio only)
}

// ParseARN parses a destination ARN.
func ParseARN(s string) (ARN, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 6 || parts[0] != "arn" {
		return ARN{}, fmt.Errorf("notify: malformed ARN %q", s)
	}
	a := ARN{Partition: parts[1], Service: parts[2], Region: parts[3]}
	switch {
	case a.Partition == "opens3" || a.Partition == "minio":
		if a.Service != "sqs" || len(parts) != 6 || parts[4] == "" || parts[5] == "" {
			return ARN{}, fmt.Errorf("notify: malformed ARN %q", s)
		}
		a.Name, a.Type = parts[4], parts[5]
	case strings.HasPrefix(a.Partition, "aws"):
		switch a.Service {
		case "sqs", "sns":
			if len(parts) != 6 || parts[5] == "" {
				return ARN{}, fmt.Errorf("notify: malformed ARN %q", s)
			}
			a.Name = parts[5]
		case "lambda":
			if len(parts) < 7 || parts[5] != "function" || parts[6] == "" {
				return ARN{}, fmt.Errorf("notify: malformed ARN %q", s)
			}
			a.Name = parts[6]
		default:
			return ARN{}, fmt.Errorf("notify: unsupported ARN service %q", a.Service)
		}
	default:
		return ARN{}, fmt.Errorf("notify: unsupported ARN partition %q", a.Partition)
	}
	return a, nil
}

// TargetARN returns the native ARN for a configured target.
func TargetARN(name, kind string) string { return "arn:opens3:sqs::" + name + ":" + kind }
