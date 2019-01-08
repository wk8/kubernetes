/*
Copyright 2019 The Kubernetes Authors.

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

// Package gmsaauthorizer contains an admission controller that, whenever
// a pod is created that requests to use a Microsoft group Managed Service
// Account (a.k.a gMSA - more doc at
// https://docs.microsoft.com/en-us/windows-server/security/group-managed-service-accounts/group-managed-service-accounts-overview),
// checks that both the user making the request and the spec's
// serviceAccountName (if any) are authorized for the `use` verb on the gMSA's
// configmap.
// It also forbids updating a pod's gMSA
package gmsaauthorizer

import (
	"fmt"
	"io"


	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/kubernetes/staging/src/k8s.io/apiserver/pkg/admission"
	api "k8s.io/kubernetes/pkg/apis/core"
	genericadmissioninit "k8s.io/apiserver/pkg/admission/initializer"
)

// PluginName indicates name of admission plugin.
const PluginName = "GMSAAuthorizer"


// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(config io.Reader) (admission.Interface, error) {
		return newGMSAAuthorizer(), nil
	})
}

// AlwaysPullImages is an implementation of admission.Interface.
// It looks at all new pods and, if they try to make use of a gMSA, check that both the user
// making the request and the spec's serviceAccountName (if any) are authorized for the `use`
// verb on the gMSA's configmap.
type GMSAAuthorizer struct {
	*admission.Handler
	authorizer            authorizer.Authorizer
}

// have the compiler check that we satisfy the right interfaces
var _ admission.ValidationInterface = &GMSAAuthorizer{}
var _ genericadmissioninit.WantsAuthorizer = &GMSAAuthorizer{}

// SetAuthorizer sets the authorizer.
func (a *GMSAAuthorizer) SetAuthorizer(authorizer authorizer.Authorizer) {
	a.authorizer = authorizer
}

// ValidateInitialization ensures an authorizer is set.
func (a *GMSAAuthorizer) ValidateInitialization() error {
	if a.authorizer == nil {
		return fmt.Errorf("%s requires an authorizer", PluginName)
	}
	return nil
}

// Validate makes sure that pods using gMSA's are created by users who are indeed authorized to
// use the requested gMSA
func (a *GMSAAuthorizer) Validate(attributes admission.Attributes) error {
	if !isPodRequest(attributes) {
		return nil
	}

	pod, credentialSpecConfig, err := extractCredentialSpecConfigFromPod(attributes.GetObject())
	if err != nil {
		return err
	}

	// forbid updates
	if attributes.GetOperation() == admission.Update {
		_, oldCredentialSpecConfig, err := extractCredentialSpecConfigFromPod(attributes.GetOldObject())
		if err != nil {
			return err
		}

		if credentialSpecConfig != oldCredentialSpecConfig {
			return apierrors.NewBadRequest("Cannot update an existing pod's spec.securityContext.windows.credentialSpecConfig field")
		}
	}

	// TODO see the podsecurityadmission!
	wkpo := authorizer.AttributesRecord{
		User:            attributes.GetUserInfo(),
		Verb:            "use",
		Namespace:       attributes.GetNamespace(),

		APIGroup:        attributes.GetResource().Group
		Resource:        attributes.GetNamespace(),
		Name:            attributes.GetName(),

		ResourceRequest: true,
	}

	// TODO wkpo check pod.Spec.ServiceAccountName?

}

func newGMSAAuthorizer() *GMSAAuthorizer{
	return &GMSAAuthorizer{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}
}

// Checks that the request involves a pod
func isPodRequest(attributes admission.Attributes) bool {
	return len(attributes.GetSubresource()) == 0 && attributes.GetResource().GroupResource() == api.Resource("pods")
}

// Casts a generic API object to a pod, and
// extracts the `spec.securityContext.windows.credentialSpecConfig` field of a its spec
// TODO wkpo on a besoin du pod??
func extractCredentialSpecConfigFromPod(object runtime.Object) (pod *api.Pod, credentialSpecConfig string, err error) {
	pod, ok := object.(*api.Pod)
	if !ok {
		return nil, "", apierrors.NewBadRequest("Resource was marked with kind Pod but was unable to be converted")
	}
	if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.WindowsSecurityOptions != nil {
		credentialSpecConfig = pod.Spec.SecurityContext.WindowsSecurityOptions.CredentialSpecConfig
	}
	return
}
