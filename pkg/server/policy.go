// Copyright 2017 uSwitch
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"fmt"
	"regexp"

	v1 "k8s.io/api/core/v1"

	"github.com/uswitch/kiam/pkg/aws/sts"
	"github.com/uswitch/kiam/pkg/k8s"
)

// AssumeRolePolicy allows for policy to check whether pods can assume the role being
// requested
type AssumeRolePolicy interface {
	IsAllowedAssumeRole(ctx context.Context, roleName string, pod *v1.Pod) (Decision, error)
}

// CompositeAssumeRolePolicy allows multiple policies to be checked
type CompositeAssumeRolePolicy struct {
	policies []AssumeRolePolicy
}

func (p *CompositeAssumeRolePolicy) IsAllowedAssumeRole(ctx context.Context, role string, pod *v1.Pod) (Decision, error) {
	for _, policy := range p.policies {
		decision, err := policy.IsAllowedAssumeRole(ctx, role, pod)
		if err != nil {
			return nil, err
		}
		if !decision.IsAllowed() {
			return decision, nil
		}
	}

	return &allowed{}, nil
}

// Creates a AssumeRolePolicy that tests all policies pass.
func Policies(p ...AssumeRolePolicy) *CompositeAssumeRolePolicy {
	return &CompositeAssumeRolePolicy{
		policies: p,
	}
}

// RequestingAnnotatedRolePolicy ensures the pod is requesting the role that it's
// currently annotated with.
type RequestingAnnotatedRolePolicy struct {
	pods     k8s.PodGetter
	resolver sts.ARNResolver
}

func NewRequestingAnnotatedRolePolicy(p k8s.PodGetter, resolver sts.ARNResolver) *RequestingAnnotatedRolePolicy {
	return &RequestingAnnotatedRolePolicy{pods: p, resolver: resolver}
}

func (p *RequestingAnnotatedRolePolicy) IsAllowedAssumeRole(ctx context.Context, role string, pod *v1.Pod) (Decision, error) {
	annotatedIdentiy, err := p.resolver.Resolve(k8s.PodRole(pod))
	if err != nil {
		return nil, err
	}
	requestedIdentity, err := p.resolver.Resolve(role)
	if err != nil {
		return nil, err
	}

	if annotatedIdentiy.Equals(requestedIdentity) {
		return &allowed{}, nil
	}

	return &forbidden{requested: role, annotated: annotatedIdentiy.Name}, nil
}

// NamespacePermittedRoleNamePolicy ensures the pod is requesting a role that
// the namespace permits in its regexp annotation.
type NamespacePermittedRoleNamePolicy struct {
	namespaces k8s.NamespaceFinder
	resolver   sts.ARNResolver
	strict     bool
}

func NewNamespacePermittedRoleNamePolicy(strictRegexp bool, n k8s.NamespaceFinder, resolver sts.ARNResolver) *NamespacePermittedRoleNamePolicy {
	return &NamespacePermittedRoleNamePolicy{namespaces: n, resolver: resolver, strict: strictRegexp}
}

func (p *NamespacePermittedRoleNamePolicy) IsAllowedAssumeRole(ctx context.Context, role string, pod *v1.Pod) (Decision, error) {
	requestedIdentity, err := p.resolver.Resolve(role)
	if err != nil {
		return nil, err
	}

	ns, err := p.namespaces.FindNamespace(ctx, pod.GetObjectMeta().GetNamespace())
	if err != nil {
		return nil, err
	}

	expression := ns.GetAnnotations()[k8s.AnnotationPermittedKey]
	if expression == "" {
		return &namespacePolicyForbidden{expression: "(empty)", role: role}, nil
	}

	var re *regexp.Regexp
	if p.strict {
		re, err = regexp.Compile("^" + expression + "$")
		if err != nil {
			return nil, err
		}
	} else {
		re, err = regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
	}

	if !re.MatchString(requestedIdentity.ARN) {
		return &namespacePolicyForbidden{expression: expression, role: requestedIdentity.ARN}, nil
	}

	return &allowed{}, nil
}

// Decision reports (with message) as to whether the assume role is permitted.
type Decision interface {
	IsAllowed() bool
	Explanation() string
}

type allowed struct {
}

func (a *allowed) IsAllowed() bool {
	return true
}

func (a *allowed) Explanation() string {
	return ""
}

type forbidden struct {
	requested string
	annotated string
}

func (f *forbidden) IsAllowed() bool {
	return false
}
func (f *forbidden) Explanation() string {
	return fmt.Sprintf("requested '%s' but annotated with '%s', forbidden", f.requested, f.annotated)
}

type namespacePolicyForbidden struct {
	expression string
	role       string
}

func (f *namespacePolicyForbidden) IsAllowed() bool {
	return false
}

func (f *namespacePolicyForbidden) Explanation() string {
	return fmt.Sprintf("namespace policy expression '%s' forbids role '%s'", f.expression, f.role)
}
