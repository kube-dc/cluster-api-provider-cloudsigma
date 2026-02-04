/*
Copyright 2025 Kube-DC Authors.

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

package cloud

import (
	"errors"
	"fmt"
)

// PermissionDeniedError indicates the impersonated user cannot access a CloudSigma resource.
// This typically happens when:
// - A VM was created by a different user/token
// - The impersonation token doesn't have the right permissions
// - There's an ACL issue on the CloudSigma side
type PermissionDeniedError struct {
	ResourceType string // "server", "drive", "ip", etc.
	UUID         string
	StatusCode   int
	User         string // impersonated user email
	Err          error
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied: user %s cannot access %s %s (HTTP %d): %v",
		e.User, e.ResourceType, e.UUID, e.StatusCode, e.Err)
}

func (e *PermissionDeniedError) Unwrap() error {
	return e.Err
}

// NewPermissionDeniedError creates a new PermissionDeniedError
func NewPermissionDeniedError(resourceType, uuid string, statusCode int, user string, err error) *PermissionDeniedError {
	return &PermissionDeniedError{
		ResourceType: resourceType,
		UUID:         uuid,
		StatusCode:   statusCode,
		User:         user,
		Err:          err,
	}
}

// IsPermissionDeniedError checks if an error is a PermissionDeniedError
func IsPermissionDeniedError(err error) bool {
	var pde *PermissionDeniedError
	return errors.As(err, &pde)
}

// GetPermissionDeniedError extracts PermissionDeniedError from an error chain
func GetPermissionDeniedError(err error) *PermissionDeniedError {
	var pde *PermissionDeniedError
	if errors.As(err, &pde) {
		return pde
	}
	return nil
}
