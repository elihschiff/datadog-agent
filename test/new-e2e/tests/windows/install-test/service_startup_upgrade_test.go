// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package installtest

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	windowsCommon "github.com/DataDog/datadog-agent/test/new-e2e/tests/windows/common"
	windowsAgent "github.com/DataDog/datadog-agent/test/new-e2e/tests/windows/common/agent"
)

//go:embed fixtures/service-startup-checkpoints.ps1
var serviceStartupCheckpoints string

func TestUpgradeServiceProtection(t *testing.T) {
	Run(t, &testUpgradeServiceProtectionSuite{})
}

type testUpgradeServiceProtectionSuite struct {
	baseAgentMSISuite
}

// Each scenario needs its own suite hooks so audit evidence is collected before
// the next install/upgrade cycle can overwrite the Security event log.
func (s *testUpgradeServiceProtectionSuite) TestRemovalRollback() {
	s.runUpgradeServiceProtection("removal_rollback", "UninstallUser", "")
}

func (s *testUpgradeServiceProtectionSuite) TestConfigurationRollback() {
	s.runUpgradeServiceProtection("configuration_rollback", "", "ConfigureServices")
}

func (s *testUpgradeServiceProtectionSuite) TestLateRollback() {
	s.runUpgradeServiceProtection("late_rollback", "", "StartDDServices")
}

func (s *testUpgradeServiceProtectionSuite) TestSuccessfulUpgrade() {
	s.runUpgradeServiceProtection("success", "", "")
}

func (s *testUpgradeServiceProtectionSuite) runUpgradeServiceProtection(name, oldFailure, newFailure string) {
	vm := s.Env().RemoteHost
	upgrade, err := windowsAgent.GetUpgradeTestPackageFromEnv()
	s.Require().NoError(err)

	s.Run(name, func() {
		directory := strings.TrimSpace(vm.MustExecute(`[System.IO.Path]::Combine($env:TEMP, [guid]::NewGuid().ToString('N'))`))
		s.Require().NoError(vm.MkdirAll(directory))
		defer vm.RemoveAll(directory) //nolint:errcheck
		scriptPath := directory + `\checkpoints.ps1`
		_, err = vm.WriteFile(scriptPath, []byte(serviceStartupCheckpoints))
		s.Require().NoError(err)

		prepare := func(role string, pkg *windowsAgent.Package, failAfter string) string {
			path := directory + `\` + role + ".msi"
			s.Require().NoError(windowsCommon.PutOrDownloadFile(vm, pkg.URL, path))
			_, err := vm.Execute(fmt.Sprintf(`& '%s' -MsiPath '%s' -OutputDirectory '%s' -Role '%s' -FailAfter '%s'`, scriptPath, path, directory, role, failAfter))
			s.Require().NoError(err)
			return path
		}
		oldMSI := prepare("old", s.AgentPackage, oldFailure)
		newMSI := prepare("new", upgrade, newFailure)
		installArgs := `APIKEY="00000000000000000000000000000000" DD_INSTALL_ONLY=1`
		s.Require().NoError(windowsCommon.InstallMSI(vm, oldMSI, installArgs, filepath.Join(s.SessionOutputDir(), "install.log")))
		oldProduct, err := windowsAgent.GetDatadogAgentProductCode(vm)
		s.Require().NoError(err)
		defer s.cleanupAgent()
		// Rollback deliberately applies the outgoing package's defaults rather
		// than restoring these customer-customized startup settings.
		vm.MustExecute(`Set-Service datadogagent -StartupType Manual; Set-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\datadogagent' -Name DelayedAutoStart -Value 0; Set-Service 'Datadog Installer' -StartupType Disabled`)
		s.Require().Len(s.captureProtectedServiceStartup(), len(protectedServiceNames))

		err = windowsCommon.InstallMSI(vm, newMSI, installArgs, filepath.Join(s.SessionOutputDir(), "upgrade.log"))
		failed := oldFailure != "" || newFailure != ""
		if failed {
			s.Require().Error(err)
		} else {
			s.Require().NoError(err)
		}

		checkpoint := func(role, action string, enabled bool) {
			data, err := vm.ReadFile(directory + `\` + role + "-" + action + ".txt")
			s.Require().NoError(err, "execution must reach %s/%s", role, action)
			lines := strings.FieldsFunc(strings.TrimSpace(string(data)), func(r rune) bool { return r == '\r' || r == '\n' })
			s.Require().Len(lines, len(protectedServiceNames))
			for _, line := range lines {
				start := "4"
				if enabled {
					start = "3"
					if strings.HasPrefix(line, "datadogagent=") {
						start = "2"
					}
				}
				s.Require().True(strings.HasSuffix(line, "="+start), "unexpected startup setting at %s/%s: %s", role, action, line)
			}
		}
		checkpoint("old", "StopDDServices", false)
		checkpoint("old", "UninstallUser", false)
		if oldFailure == "" {
			// Registrations must survive removal even after DeleteServices.
			checkpoint("old", "DeleteServices", false)
			checkpoint("new", "ConfigureUser", false)
			checkpoint("new", "ConfigureServices", false)
			if newFailure != "ConfigureServices" {
				checkpoint("new", "StartDDServices", true)
			}
		}
		product, err := windowsAgent.GetDatadogAgentProductCode(vm)
		s.Require().NoError(err)
		if failed {
			s.Require().Equal(oldProduct, product, "rollback must restore the outgoing product")
			s.assertServiceStartupDefaults()
			// Default enablement must leave a usable installation after permissions rollback.
			s.Require().NoError(windowsCommon.StartService(vm, "datadogagent"))
			RequireAgentVersionRunningWithNoErrors(s.T(), s.NewTestClientForHost(vm), s.AgentPackage.AgentVersion())
		} else {
			s.Require().NotEqual(oldProduct, product, "must exercise a major upgrade, not repair")
			s.assertServiceStartupDefaults()
		}
		s.Require().NoError(windowsCommon.UninstallMSI(vm, product, "", filepath.Join(s.SessionOutputDir(), "uninstall.log")))
		s.Require().Empty(s.captureProtectedServiceStartup(), "true uninstall must delete service registrations")
	})
}

// Fleet removes the MSI between package versions. Its removal must retain
// disabled registrations, and an explicit Fleet purge must remove them.
func (s *testUpgradeServiceProtectionSuite) TestFleetRemovalKeepsServices() {
	vm := s.Env().RemoteHost
	msi := s.installAgentPackage(vm, s.AgentPackage, windowsAgent.WithInstallOnly("1"))
	_, agentUser, err := windowsAgent.GetAgentUserFromRegistry(vm)
	s.Require().NoError(err)
	configDir, err := windowsAgent.GetConfigRootFromRegistry(vm)
	s.Require().NoError(err)
	defer func() {
		// A failed assertion between Fleet removal and reinstall must not leave
		// orphaned registrations that break a retry on the same VM.
		if _, err := windowsAgent.GetDatadogAgentProductCode(vm); err != nil && len(s.captureProtectedServiceStartup()) != 0 {
			s.Assert().NoError(windowsCommon.InstallMSI(vm, msi, `DD_INSTALL_ONLY=1`, filepath.Join(s.SessionOutputDir(), "cleanup-reinstall.log")))
		}
		if _, err := windowsAgent.GetDatadogAgentProductCode(vm); err == nil {
			s.cleanupAgent()
		} else {
			s.Assert().NoError(windowsCommon.RemoveLocalUser(vm, agentUser))
			s.Assert().NoError(vm.RemoveAll(configDir))
		}
	}()
	product, err := windowsAgent.GetDatadogAgentProductCode(vm)
	s.Require().NoError(err)

	s.Require().NoError(windowsCommon.UninstallMSI(vm, product, "FLEET_INSTALL=1", filepath.Join(s.SessionOutputDir(), "fleet-uninstall.log")))
	retained := s.captureProtectedServiceStartup()
	s.Require().Len(retained, len(protectedServiceNames))
	for name, setting := range retained {
		s.Require().Equal(windowsCommon.SERVICE_DISABLED, setting.startType, "%s must remain disabled between Fleet packages", name)
	}

	s.Require().NoError(windowsCommon.InstallMSI(vm, msi, `DD_INSTALL_ONLY=1 FLEET_INSTALL=1`, filepath.Join(s.SessionOutputDir(), "fleet-reinstall.log")))
	s.assertServiceStartupDefaults()
	s.Require().NoError(windowsCommon.StartService(vm, "datadogagent"))
	RequireAgentVersionRunningWithNoErrors(s.T(), s.NewTestClientForHost(vm), s.AgentPackage.AgentVersion())

	s.Require().NoError(windowsCommon.UninstallMSI(vm, product, "FLEET_INSTALL=1 FLEET_PURGE=1", filepath.Join(s.SessionOutputDir(), "fleet-purge.log")))
	s.Require().Empty(s.captureProtectedServiceStartup(), "Fleet purge must delete retained service registrations")
}

func (s *baseAgentMSISuite) assertServiceStartupDefaults() {
	actual := s.captureProtectedServiceStartup()
	s.Require().Len(actual, len(protectedServiceNames))
	for name, setting := range actual {
		if name == "datadogagent" {
			s.Assert().Equal(windowsCommon.SERVICE_AUTO_START, setting.startType, name)
			s.Assert().Equal("1", setting.delayedAutoStart, name)
		} else {
			s.Assert().Equal(windowsCommon.SERVICE_DEMAND_START, setting.startType, name)
		}
	}
}
