package main

import "testing"

func validManifest() manifest {
	return manifest{
		Modules: []module{{Name: "chat", APIImport: "api", ImplementationImport: "impl", TransportImport: "transport", ImplementationType: "Messages"}},
		Targets: []target{{Name: "monolith", Mode: "monolith", Storage: "dqlite", Processes: map[string][]string{"app": {"chat"}}, Replicas: map[string]int{"app": 3}}},
	}
}

func TestValidateAcceptsIndependentReplicaCounts(t *testing.T) {
	value := validManifest()
	value.Targets = append(value.Targets, target{
		Name:      "separate",
		Mode:      "separate",
		Storage:   "dqlite",
		Processes: map[string][]string{"http": {}, "chat": {"chat"}},
		Replicas:  map[string]int{"http": 3, "chat": 2},
	})
	if err := validate(value); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestValidateRejectsReplicaCountForUnknownProcess(t *testing.T) {
	value := validManifest()
	value.Targets[0].Replicas["worker"] = 1
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted replica count for unknown process")
	}
}

func TestValidateRejectsDuplicateTargets(t *testing.T) {
	value := validManifest()
	value.Targets = append(value.Targets, value.Targets[0])
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted duplicate target")
	}
}

func TestValidateRejectsUnnamedTarget(t *testing.T) {
	value := validManifest()
	value.Targets[0].Name = ""
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted unnamed target")
	}
}

func TestValidateRejectsMemoryStorageForReplicas(t *testing.T) {
	value := validManifest()
	value.Targets[0].Storage = "memory"
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted memory storage for replicated target")
	}
}

func TestValidateRejectsImplicitStorageSelection(t *testing.T) {
	value := validManifest()
	value.Targets[0].Storage = ""
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted missing storage selection")
	}
}

func TestValidateRejectsMonolithWithMultipleProcesses(t *testing.T) {
	value := validManifest()
	value.Targets[0].Processes["worker"] = []string{}
	value.Targets[0].Replicas["worker"] = 1
	if err := validate(value); err == nil {
		t.Fatal("validate() accepted a monolith split across processes")
	}
}
