/*
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

package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"

	"sigs.k8s.io/karpenter/pkg/apis/v1beta1"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
)

// Convert v1 NodePool to v1beta1 NodePool
func (in *NodePool) ConvertTo(ctx context.Context, to apis.Convertible) error {
	v1beta1NP := to.(*v1beta1.NodePool)
	v1beta1NP.ObjectMeta = in.ObjectMeta

	// Convert v1 status
	v1beta1NP.Status.Resources = in.Status.Resources
	if err := in.Spec.convertTo(&v1beta1NP.Spec, in.Annotations[KubeletCompatibilityAnnotationKey], in.Annotations[NodeClassReferenceAnnotationKey]); err != nil {
		return err
	}
	// Remove the annotations from the v1beta1 NodeClaim on the convert back
	delete(v1beta1NP.Annotations, KubeletCompatibilityAnnotationKey)
	delete(v1beta1NP.Annotations, NodeClassReferenceAnnotationKey)
	return nil
}

func (in *NodePoolSpec) convertTo(v1beta1np *v1beta1.NodePoolSpec, kubeletAnnotation, nodeClassReferenceAnnotation string) error {
	v1beta1np.Weight = in.Weight
	v1beta1np.Limits = v1beta1.Limits(in.Limits)
	in.Disruption.convertTo(&v1beta1np.Disruption)
	// Set the expireAfter to the nodeclaim template's expireAfter.
	// Don't convert terminationGracePeriod, as this is only included in v1.
	v1beta1np.Disruption.ExpireAfter = v1beta1.NillableDuration(in.Template.Spec.ExpireAfter)
	return in.Template.convertTo(&v1beta1np.Template, kubeletAnnotation, nodeClassReferenceAnnotation)
}

func (in *Disruption) convertTo(v1beta1np *v1beta1.Disruption) {
	v1beta1np.ConsolidationPolicy = lo.Ternary(in.ConsolidationPolicy == ConsolidationPolicyWhenEmptyOrUnderutilized,
		v1beta1.ConsolidationPolicyWhenUnderutilized, v1beta1.ConsolidationPolicy(in.ConsolidationPolicy))
	// If the v1 nodepool is WhenEmptyOrUnderutilized, the v1beta1 nodepool should have an unset consolidateAfter
	v1beta1np.ConsolidateAfter = lo.Ternary(in.ConsolidationPolicy == ConsolidationPolicyWhenEmptyOrUnderutilized,
		nil, (*v1beta1.NillableDuration)(lo.ToPtr(in.ConsolidateAfter)))
}

func (in *NodeClaimTemplate) convertTo(v1beta1np *v1beta1.NodeClaimTemplate, kubeletAnnotation, nodeClassReferenceAnnotation string) error {
	v1beta1np.ObjectMeta = v1beta1.ObjectMeta(in.ObjectMeta)
	v1beta1np.Spec.Taints = in.Spec.Taints
	v1beta1np.Spec.StartupTaints = in.Spec.StartupTaints
	v1beta1np.Spec.Requirements = lo.Map(in.Spec.Requirements, func(v1Requirements NodeSelectorRequirementWithMinValues, _ int) v1.NodeSelectorRequirement {
		return v1.NodeSelectorRequirement{
			Key:      v1Requirements.Key,
			Values:   v1Requirements.Values,
			Operator: v1Requirements.Operator,
		}
	})
	// Convert the NodeClassReference depending on whether the annotation exists
	v1beta1np.Spec.NodeClassRef = &v1beta1.NodeClassReference{}
	if nodeClassReferenceAnnotation != "" {
		if err := json.Unmarshal([]byte(nodeClassReferenceAnnotation), v1beta1np.Spec.NodeClassRef); err != nil {
			return fmt.Errorf("unmarshaling nodeClassRef annotation, %w", err)
		}
	} else {
		v1beta1np.Spec.NodeClassRef.Name = in.Spec.NodeClassRef.Name
		v1beta1np.Spec.NodeClassRef.Kind = in.Spec.NodeClassRef.Kind
	}
	if kubeletAnnotation != "" {
		v1beta1kubelet := &v1beta1.KubeletConfiguration{}
		err := json.Unmarshal([]byte(kubeletAnnotation), v1beta1kubelet)
		if err != nil {
			return fmt.Errorf("unmarshaling kubelet config annotation, %w", err)

		}
		v1beta1np.Spec.Kubelet = v1beta1kubelet
	}
	return nil
}

// Convert v1beta1 NodePool to V1 NodePool
func (in *NodePool) ConvertFrom(ctx context.Context, v1beta1np apis.Convertible) error {
	v1beta1NP := v1beta1np.(*v1beta1.NodePool)
	in.ObjectMeta = v1beta1NP.ObjectMeta

	// Convert v1beta1 status
	in.Status.Resources = v1beta1NP.Status.Resources

	kubeletAnnotation, err := in.Spec.convertFrom(ctx, &v1beta1NP.Spec)
	if err != nil {
		return err
	}
	if kubeletAnnotation == "" {
		in.Annotations = lo.OmitByKeys(in.Annotations, []string{KubeletCompatibilityAnnotationKey})
	} else {
		in.Annotations = lo.Assign(in.Annotations, map[string]string{KubeletCompatibilityAnnotationKey: kubeletAnnotation})
	}
	nodeClassRefAnnotation, err := json.Marshal(v1beta1NP.Spec.Template.Spec.NodeClassRef)
	if err != nil {
		return fmt.Errorf("marshaling nodeClassRef annotation, %w", err)
	}
	in.Annotations = lo.Assign(in.Annotations, map[string]string{
		NodeClassReferenceAnnotationKey: string(nodeClassRefAnnotation),
	})
	return nil
}

func (in *NodePoolSpec) convertFrom(ctx context.Context, v1beta1np *v1beta1.NodePoolSpec) (string, error) {
	in.Weight = v1beta1np.Weight
	in.Limits = Limits(v1beta1np.Limits)
	in.Template.Spec.ExpireAfter = NillableDuration(v1beta1np.Disruption.ExpireAfter)
	in.Disruption.convertFrom(&v1beta1np.Disruption)
	return in.Template.convertFrom(ctx, &v1beta1np.Template)
}

func (in *Disruption) convertFrom(v1beta1np *v1beta1.Disruption) {
	// if consolidationPolicy is WhenUnderutilized, set the v1 duration to 0, otherwise, set to the value of consolidateAfter.
	in.ConsolidateAfter = lo.Ternary(v1beta1np.ConsolidationPolicy == v1beta1.ConsolidationPolicyWhenUnderutilized,
		NillableDuration{Duration: lo.ToPtr(time.Duration(0))}, (NillableDuration)(lo.FromPtr(v1beta1np.ConsolidateAfter)))
	in.ConsolidationPolicy = lo.Ternary(v1beta1np.ConsolidationPolicy == v1beta1.ConsolidationPolicyWhenUnderutilized,
		ConsolidationPolicyWhenEmptyOrUnderutilized, ConsolidationPolicy(v1beta1np.ConsolidationPolicy))
}

func (in *NodeClaimTemplate) convertFrom(ctx context.Context, v1beta1np *v1beta1.NodeClaimTemplate) (string, error) {
	in.ObjectMeta = ObjectMeta(v1beta1np.ObjectMeta)
	in.Spec.Taints = v1beta1np.Spec.Taints
	in.Spec.StartupTaints = v1beta1np.Spec.StartupTaints
	in.Spec.Requirements = lo.Map(v1beta1np.Spec.Requirements, func(v1beta1Requirements v1.NodeSelectorRequirement, _ int) NodeSelectorRequirementWithMinValues {
		return NodeSelectorRequirementWithMinValues{
			NodeSelectorRequirement: v1.NodeSelectorRequirement{
				Key:      v1beta1Requirements.Key,
				Values:   v1beta1Requirements.Values,
				Operator: v1beta1Requirements.Operator,
			},
		}
	})

	defaultNodeClassGVK := injection.GetNodeClasses(ctx)[0]
	in.Spec.NodeClassRef = &NodeClassReference{
		Name:  v1beta1np.Spec.NodeClassRef.Name,
		Kind:  lo.Ternary(v1beta1np.Spec.NodeClassRef.Kind == "", defaultNodeClassGVK.Kind, v1beta1np.Spec.NodeClassRef.Kind),
		Group: lo.Ternary(v1beta1np.Spec.NodeClassRef.APIVersion == "", defaultNodeClassGVK.Group, strings.Split(v1beta1np.Spec.NodeClassRef.APIVersion, "/")[0]),
	}
	if v1beta1np.Spec.Kubelet != nil {
		kubelet, err := json.Marshal(v1beta1np.Spec.Kubelet)
		if err != nil {
			return "", fmt.Errorf("marshaling kubelet config annotation, %w", err)
		}
		return string(kubelet), nil
	}
	return "", nil
}
