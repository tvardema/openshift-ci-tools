/*
Copyright The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1beta1 "k8s.io/api/storage/v1beta1"
	storagev1beta1 "k8s.io/client-go/applyconfigurations/storage/v1beta1"
	gentype "k8s.io/client-go/gentype"
	typedstoragev1beta1 "k8s.io/client-go/kubernetes/typed/storage/v1beta1"
)

// fakeVolumeAttributesClasses implements VolumeAttributesClassInterface
type fakeVolumeAttributesClasses struct {
	*gentype.FakeClientWithListAndApply[*v1beta1.VolumeAttributesClass, *v1beta1.VolumeAttributesClassList, *storagev1beta1.VolumeAttributesClassApplyConfiguration]
	Fake *FakeStorageV1beta1
}

func newFakeVolumeAttributesClasses(fake *FakeStorageV1beta1) typedstoragev1beta1.VolumeAttributesClassInterface {
	return &fakeVolumeAttributesClasses{
		gentype.NewFakeClientWithListAndApply[*v1beta1.VolumeAttributesClass, *v1beta1.VolumeAttributesClassList, *storagev1beta1.VolumeAttributesClassApplyConfiguration](
			fake.Fake,
			"",
			v1beta1.SchemeGroupVersion.WithResource("volumeattributesclasses"),
			v1beta1.SchemeGroupVersion.WithKind("VolumeAttributesClass"),
			func() *v1beta1.VolumeAttributesClass { return &v1beta1.VolumeAttributesClass{} },
			func() *v1beta1.VolumeAttributesClassList { return &v1beta1.VolumeAttributesClassList{} },
			func(dst, src *v1beta1.VolumeAttributesClassList) { dst.ListMeta = src.ListMeta },
			func(list *v1beta1.VolumeAttributesClassList) []*v1beta1.VolumeAttributesClass {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1beta1.VolumeAttributesClassList, items []*v1beta1.VolumeAttributesClass) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
