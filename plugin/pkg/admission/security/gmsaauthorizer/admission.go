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

// Package gmsaauthorizer contains an admission controller that enables support for
// Microsoft's group Managed Service Accounts (a.k.a gMSA - more doc at
// https://docs.microsoft.com/en-us/windows-server/security/group-managed-service-accounts/group-managed-service-accounts-overview)
package gmsaauthorizer

import (
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	genericadmissioninit "k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/klog"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/serviceaccount"
)

const (
	// PluginName indicates name of admission plugin.
	PluginName = "GMSAAuthorizer"

	// For the alpha version of gMSA support, we use annotations rather than an API field
	// gMSAConfigMapAnnotationKey is set by users, and should give the name of the configmap
	// containing the gMSA's cred spec
	gMSAConfigMapAnnotationKey = "pod.alpha.kubernetes.io/windows-gmsa-config-map"
	// gMSACredSpecAnnotationKey's use is restricted to this admission controller, and any
	// value set by the user will be silently overwritten
	gMSACredSpecAnnotationKey = "pod.alpha.kubernetes.io/windows-gmsa-cred-spec"
)

// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(config io.Reader) (admission.Interface, error) {
		return newGMSAAuthorizer(), nil
	})
}

// A GMSAAuthorizer looks at all new pods and, if they try to make use of a gMSA, check that
// their spec's serviceAccountName are authorized for the `use`
// verb on the gMSA's configmap.
// TODO wkpo comment?
type GMSAAuthorizer struct {
	*admission.Handler
	authorizer authorizer.Authorizer
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

	pod, configMapName, err := extractConfigMapNameFromPod(attributes.GetObject())
	if err != nil {
		return err
	}

	// forbid updates
	// TODO wkpo: what happens then? restart loop? error message?
	if attributes.GetOperation() == admission.Update {
		_, oldConfigMapName, err := extractConfigMapNameFromPod(attributes.GetOldObject())
		if err != nil {
			return err
		}

		if configMapName != oldConfigMapName {
			return apierrors.NewBadRequest(fmt.Sprintf("Cannot update an existing pod's %s annotation", gMSAConfigMapAnnotationKey))
		}
	}

	if configMapName == "" {
		// nothing to do, let's still make sure there's no cred spec inlined
		delete(pod.Annotations, gMSACredSpecAnnotationKey)
		return nil
	}

	// check that the associated service account can read the relevant service map
	if !a.isAuthorizedToReadConfigMap(attributes, pod, configMapName) {
		return apierrors.NewForbidden(schema.GroupResource{Resource: string(corev1.ResourceConfigMaps)},
			configMapName,
			fmt.Errorf(fmt.Sprintf("service account %s does not have access to the config map at %s", pod.Spec.ServiceAccountName, configMapName)))
	}

	// finally inline the config map's contents into the spec

}

func newGMSAAuthorizer() *GMSAAuthorizer {
	return &GMSAAuthorizer{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}
}

// isPodRequest checks that the request involves a pod
func isPodRequest(attributes admission.Attributes) bool {
	return len(attributes.GetSubresource()) == 0 && attributes.GetResource().GroupResource() == api.Resource("pods")
}

// extractConfigMapNameFromPod casts a generic API object to a pod, and
// extracts the name of the config map containing the gMSA cred spec, if any
// TODO wkpo on a besoin du pod??
func extractConfigMapNameFromPod(object runtime.Object) (*api.Pod, string, error) {
	pod, ok := object.(*api.Pod)
	if !ok {
		return nil, "", apierrors.NewBadRequest("Resource was marked with kind Pod but was unable to be converted")
	}
	return pod, pod.Annotations[gMSAConfigMapAnnotationKey], nil
}

// TODO wkpo: read? use?
// isAuthorizedToReadConfigMap checks that the service account is authorized to read that config map
func (a *GMSAAuthorizer) isAuthorizedToReadConfigMap(attributes admission.Attributes, pod *api.Pod, configMapName string) bool {
	var serviceAccountInfo user.Info
	if pod.Spec.ServiceAccountName != "" {
		// TODO wkpo: what happens if there's no service account in the spec? should we deny then?
		serviceAccountInfo = serviceaccount.UserInfo(attributes.GetNamespace(), pod.Spec.ServiceAccountName, "")
	}

	authorizerAttributes := authorizer.AttributesRecord{
		User:      serviceAccountInfo,
		Verb:      "use",
		Namespace: attributes.GetNamespace(),

		// TODO wkpo c bon ca?
		// APIGroup:        attributes.GetResource().Group
		Resource: string(corev1.ResourceConfigMaps), // TODO wkpo ? "configmap",
		Name:     configMapName,

		ResourceRequest: true,
	}

	decision, reason, err := a.authorizer.Authorize(authorizerAttributes)
	if err != nil {
		klog.V(5).Infof("cannot authorize for policy: %v,%v", reason, err)
	}
	return decision == authorizer.DecisionAllow
}
