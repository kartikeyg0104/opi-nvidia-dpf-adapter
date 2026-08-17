/*
Copyright 2026 Kartikey Gupta.

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

package controller

// DataProcessingUnit is owned by openshift/dpu-operator (config.openshift.io/v1).
// This companion repo does not serve that CRD. The watch and the OPI→DPF field
// mapping are declared in config/mappings/dataprocessingunit.yaml and run by
// TranslationReconciler.
