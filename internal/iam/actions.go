package iam

import (
	"fmt"
	"sort"
	"strings"
)

// Administrative actions use AWS's vocabulary (IAM, KMS, STS) so that
// policies written for AWS mean the same thing here. The few operations
// AWS has no equivalent for live under "opens3:". MinIO's "admin:*" names
// are not accepted; a policy that uses one is rejected with the AWS name.
const (
	ActionListUsers          = "iam:ListUsers"
	ActionGetUser            = "iam:GetUser"
	ActionCreateUser         = "iam:CreateUser"
	ActionDeleteUser         = "iam:DeleteUser"
	ActionUpdateUser         = "iam:UpdateUser"
	ActionAttachUserPolicy   = "iam:AttachUserPolicy"
	ActionDetachUserPolicy   = "iam:DetachUserPolicy"
	ActionListAttachedUser   = "iam:ListAttachedUserPolicies"
	ActionCreateLoginProfile = "iam:CreateLoginProfile"
	ActionUpdateLoginProfile = "iam:UpdateLoginProfile"
	ActionDeleteLoginProfile = "iam:DeleteLoginProfile"
	ActionGetLoginProfile    = "iam:GetLoginProfile"
	ActionChangePassword     = "iam:ChangePassword"
	ActionListAccessKeys     = "iam:ListAccessKeys"
	ActionCreateAccessKey    = "iam:CreateAccessKey"
	ActionUpdateAccessKey    = "iam:UpdateAccessKey"
	ActionDeleteAccessKey    = "iam:DeleteAccessKey"
	ActionListGroups         = "iam:ListGroups"
	ActionGetGroup           = "iam:GetGroup"
	ActionCreateGroup        = "iam:CreateGroup"
	ActionUpdateGroup        = "iam:UpdateGroup"
	ActionDeleteGroup        = "iam:DeleteGroup"
	ActionAddUserToGroup     = "iam:AddUserToGroup"
	ActionRemoveUserFromGrp  = "iam:RemoveUserFromGroup"
	ActionListGroupsForUser  = "iam:ListGroupsForUser"
	ActionAttachGroupPolicy  = "iam:AttachGroupPolicy"
	ActionDetachGroupPolicy  = "iam:DetachGroupPolicy"
	ActionListAttachedGroup  = "iam:ListAttachedGroupPolicies"
	ActionListPolicies       = "iam:ListPolicies"
	ActionGetPolicy          = "iam:GetPolicy"
	ActionGetPolicyVersion   = "iam:GetPolicyVersion"
	ActionCreatePolicy       = "iam:CreatePolicy"
	ActionDeletePolicy       = "iam:DeletePolicy"
	ActionGetAccountSummary  = "iam:GetAccountSummary"
	ActionKMSListKeys        = "kms:ListKeys"
	ActionKMSCreateKey       = "kms:CreateKey"
	ActionKMSDeleteKey       = "kms:ScheduleKeyDeletion"
	ActionSTSAssumeRole      = "sts:AssumeRole"
	ActionServerInfo         = "opens3:ServerInfo"
	ActionHealth             = "opens3:Health"
	ActionMetrics            = "opens3:Metrics"
)

// legacyActions maps MinIO-style admin actions to their AWS equivalents,
// for error messages only.
var legacyActions = map[string]string{
	"admin:ServerInfo": ActionServerInfo, "admin:Health": ActionHealth, "admin:Prometheus": ActionMetrics, "admin:Metrics": ActionMetrics,
	"admin:ListUsers": ActionListUsers, "admin:GetUser": ActionGetUser, "admin:AddUser": ActionCreateUser, "admin:CreateUser": ActionCreateUser,
	"admin:RemoveUser": ActionDeleteUser, "admin:DeleteUser": ActionDeleteUser, "admin:EnableUser": ActionUpdateUser, "admin:DisableUser": ActionUpdateUser,
	"admin:UpdateUser": ActionUpdateUser, "admin:SetUserPolicies": ActionAttachUserPolicy, "admin:SetUserPassword": ActionUpdateLoginProfile,
	"admin:ListKeys": ActionListAccessKeys, "admin:GetKey": ActionListAccessKeys, "admin:AddKey": ActionCreateAccessKey, "admin:CreateKey": ActionCreateAccessKey,
	"admin:RemoveKey": ActionDeleteAccessKey, "admin:DeleteKey": ActionDeleteAccessKey, "admin:EnableKey": ActionUpdateAccessKey, "admin:DisableKey": ActionUpdateAccessKey,
	"admin:RotateKey": ActionUpdateAccessKey, "admin:UpdateKey": ActionUpdateAccessKey, "admin:CreateServiceAccount": ActionCreateAccessKey,
	"admin:ListServiceAccounts": ActionListAccessKeys, "admin:RemoveServiceAccount": ActionDeleteAccessKey, "admin:UpdateServiceAccount": ActionUpdateAccessKey,
	"admin:ListGroups": ActionListGroups, "admin:GetGroup": ActionGetGroup, "admin:AddGroup": ActionCreateGroup, "admin:CreateGroup": ActionCreateGroup,
	"admin:UpdateGroup": ActionUpdateGroup, "admin:RemoveGroup": ActionDeleteGroup, "admin:DeleteGroup": ActionDeleteGroup, "admin:UpdateGroupMembers": ActionAddUserToGroup,
	"admin:AddUserToGroup": ActionAddUserToGroup, "admin:RemoveUserFromGroup": ActionRemoveUserFromGrp, "admin:EnableGroup": ActionUpdateGroup, "admin:DisableGroup": ActionUpdateGroup,
	"admin:ListUserPolicies": ActionListPolicies, "admin:ListPolicies": ActionListPolicies, "admin:GetPolicy": ActionGetPolicy, "admin:AddPolicy": ActionCreatePolicy,
	"admin:CreatePolicy": ActionCreatePolicy, "admin:PutPolicy": ActionCreatePolicy, "admin:RemovePolicy": ActionDeletePolicy, "admin:DeletePolicy": ActionDeletePolicy,
	"admin:AttachUserOrGroupPolicy": ActionAttachUserPolicy, "admin:ListBuckets": "s3:ListAllMyBuckets", "admin:RemoveBucket": "s3:DeleteBucket",
	"admin:ListKMSKeys": ActionKMSListKeys, "admin:CreateKMSKey": ActionKMSCreateKey, "admin:DeleteKMSKey": ActionKMSDeleteKey, "admin:KMSCreateKey": ActionKMSCreateKey,
	"admin:*": "iam:*, kms:*, sts:*, opens3:*",
}

// LegacyActionError returns a helpful error if action uses the removed
// MinIO "admin:" namespace, else nil.
func LegacyActionError(action string) error {
	if !strings.HasPrefix(strings.ToLower(action), "admin:") {
		return nil
	}
	if aws, ok := legacyActions[action]; ok {
		return fmt.Errorf("%w: action %q is not supported; use %s", ErrInvalid, action, aws)
	}
	keys := make([]string, 0, len(legacyActions))
	for k := range legacyActions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Errorf("%w: action %q is not supported; administrative actions use the AWS names (iam:*, kms:*, sts:*, opens3:*)", ErrInvalid, action)
}

// IsAdministrative reports whether an action is granted by identity
// policies only (never by bucket policies or ACLs).
func IsAdministrative(action string) bool {
	for _, p := range []string{"iam:", "kms:", "sts:", "opens3:"} {
		if strings.HasPrefix(action, p) {
			return true
		}
	}
	return false
}
