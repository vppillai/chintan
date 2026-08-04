// Package systemid holds the frozen infrastructure namespace.
//
// §7.3 draws a line this package exists to make unmissable: the brand is
// configurable and the system identifier is not.
//
//	Chintan     — what the product is called. Lives in config, changes freely.
//	voicenotes  — what the infrastructure is called. Frozen. Users never see it.
//
// The separation is not tidiness. DynamoDB tables cannot be renamed, IAM role
// names are effectively immutable, and cost-allocation tags do not backfill
// (G-023), so naming infrastructure after the brand turns any rebrand into a
// full migration or a permanent mismatch (G-056). Keeping a descriptive,
// deliberately non-commercial identifier means the brand can change and nothing
// in AWS has to move.
//
// This value is a constant rather than a config key on purpose. A config key
// invites editing, and §7.3 is explicit: "system_id must not change after Phase
// 0. Changing it means recreating and migrating everything."
package systemid

// ID is the frozen system identifier. Used for AWS resource names, the Project
// tag, SSM parameter paths, DynamoDB table names, IAM role names, the Resource
// Group, and CI stack names.
//
// Do not change this. Do not make it configurable. See §7.3.
const ID = "voicenotes"

// TagKeyProject is the tag key every resource carries (I9, §6.4). The ABAC deny
// policies and the teardown Resource Group both key on it (§9.5, §10.3), so it
// is an enforced control rather than a convention.
const TagKeyProject = "Project"

// TableName returns the DynamoDB table name for an instance.
//
// The instance suffix is not optional: a bare table name cannot exist twice in
// one account, so dev and prod would collide on the first parallel deploy
// (§6.3).
func TableName(instance string) string { return ID + "-" + instance }

// StackName returns the CloudFormation stack name for an instance.
func StackName(instance string) string { return ID + "-" + instance }

// BootstrapStackName returns the shared, manually-deployed bootstrap stack name.
// Separate from the per-instance stack because it creates the CI role itself, so
// a push to it must not trigger the instance matrix.
func BootstrapStackName() string { return ID + "-bootstrap" }

// BucketName returns the per-instance S3 bucket name.
//
// Account and region are part of the name rather than decoration: S3 bucket
// names are globally unique across all AWS accounts, so a name without them
// collides with any other deployment of this repo (§6.2).
func BucketName(instance, accountID, region string) string {
	return ID + "-" + instance + "-" + accountID + "-" + region
}

// SecretPath returns the SSM Parameter Store path for a named provider secret.
//
// Returns a path, never a value. The agent writes paths into config and
// references them; only the Lambda execution role reads what they point at, and
// kms:Decrypt on these paths is denied to the agent principal (§9.4). There is
// no development task that requires seeing an API key.
func SecretPath(instance, name string) string {
	return "/" + ID + "/" + instance + "/" + name
}
