package apitypes

import "time"

// Storage is a storage connection (ADR 0034): an S3-compatible bucket,
// defined once and used by what keeps data in object storage (the
// registry's images, for now). Its keys are never returned.
type Storage struct {
	Name     string `json:"name" example:"hetzner-images"`
	Endpoint string `json:"endpoint" example:"https://fsn1.your-objectstorage.com"`
	// Region signs requests; empty is us-east-1, which most S3-compatible
	// stores accept.
	Region string `json:"region"`
	Bucket string `json:"bucket" example:"dock-images"`
	// PathStyle addresses the bucket in the URL's path
	// (https://endpoint/bucket) instead of its host.
	PathStyle bool `json:"path_style"`
	// UsedBy names what keeps data in it: "registry".
	UsedBy    []string  `json:"used_by"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// CreateStorageRequest adds a storage connection. Dock checks that it can
// list the bucket before saving it.
type CreateStorageRequest struct {
	Name            string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
	Endpoint        string `json:"endpoint" doc:"the store's base URL, without the bucket" example:"https://fsn1.your-objectstorage.com"`
	Region          string `json:"region,omitempty" doc:"empty signs as us-east-1"`
	Bucket          string `json:"bucket"`
	PathStyle       *bool  `json:"path_style,omitempty" doc:"address the bucket in the path (https://endpoint/bucket); default true, which every S3-compatible store accepts"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// UpdateStorageRequest changes a storage connection. Fields that aren't
// set stay as they are. While something keeps data in it, only the name,
// region, addressing and keys can change.
type UpdateStorageRequest struct {
	// Name renames the connection; what uses it follows.
	Name            *string `json:"name,omitempty" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
	Endpoint        *string `json:"endpoint,omitempty"`
	Region          *string `json:"region,omitempty"`
	Bucket          *string `json:"bucket,omitempty"`
	PathStyle       *bool   `json:"path_style,omitempty"`
	AccessKeyID     *string `json:"access_key_id,omitempty"`
	SecretAccessKey *string `json:"secret_access_key,omitempty"`
}
