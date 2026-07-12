import {
  GetState,
  LoadUserConfig,
  SaveUserConfig,
  StartProxy,
  StopProxy,
} from "@bindings/cursor/internal/bridge/proxyservice.js";
import {
  GetAdRuntime,
  OpenExternalURL as OpenAdExternalURL,
} from "@bindings/cursor/internal/bridge/adservice.js";
import { GetHomeMetricsSummary, ResetHomeMetrics } from "@bindings/cursor/internal/bridge/metricsservice.js";
import {
  CheckForUpdates,
  GetAppVersion,
  GetFooterAuthorInfo,
  InstallReadyUpdate,
  GetModelEditorContext,
  OpenConfigWindow,
  OpenFooterAuthorHome,
  OpenHistoryWindow,
  OpenModelConfigWindow,
  OpenModelEditorWindow,
} from "@bindings/cursor/internal/bridge/windowservice.js";
import { Call } from "@wailsio/runtime";

const API_LOG_PREFIX = "[clientApi]";
const PROXY_SERVICE_NAME = "cursor/internal/bridge.ProxyService";

function logSuccess(name, payload, result) {
  if (import.meta.env.DEV) {
    console.log(`${API_LOG_PREFIX} ${name} response`, {
      payload,
      result,
    });
  }
}

function logError(name, payload, error) {
  if (import.meta.env.DEV) {
    console.error(`${API_LOG_PREFIX} ${name} error`, {
      payload,
      error,
    });
  }
}

function withApiLogging(name, payload, runner) {
  return Promise.resolve()
    .then(() => runner())
    .then((result) => {
      logSuccess(name, payload, result);
      return result;
    })
    .catch((error) => {
      logError(name, payload, error);
      throw error;
    });
}

export function loadUserConfig() {
  return withApiLogging("LoadUserConfig", undefined, () => LoadUserConfig());
}

export function saveUserConfig(payload) {
  return withApiLogging("SaveUserConfig", payload, () => SaveUserConfig(payload));
}

export function getProxyState() {
  return withApiLogging("GetState", undefined, () => GetState());
}

export function getHomeMetricsSummary() {
  return withApiLogging("GetHomeMetricsSummary", undefined, () => GetHomeMetricsSummary());
}

export function resetHomeMetrics() {
  return withApiLogging("ResetHomeMetrics", undefined, () => ResetHomeMetrics());
}

export function getAdRuntime() {
  return withApiLogging("GetAdRuntime", undefined, () => GetAdRuntime());
}

export function openAdExternalURL(url) {
  return withApiLogging("OpenAdExternalURL", { url }, () => OpenAdExternalURL(url));
}

export function startProxyService() {
  return withApiLogging("StartProxy", undefined, () => StartProxy());
}

export function stopProxyService() {
  return withApiLogging("StopProxy", undefined, () => StopProxy());
}

export function openLogsDirectory() {
  return withApiLogging("OpenHistoryWindow", undefined, () => OpenHistoryWindow());
}

export function openConfigWindow() {
  return withApiLogging("OpenConfigWindow", undefined, () => OpenConfigWindow());
}

export function getAppVersion() {
  return withApiLogging("GetAppVersion", undefined, () => GetAppVersion());
}

export function getFooterAuthorInfo() {
  return withApiLogging("GetFooterAuthorInfo", undefined, () => GetFooterAuthorInfo());
}

export function checkForUpdates() {
  return withApiLogging("CheckForUpdates", undefined, () => CheckForUpdates());
}

export function installReadyUpdate() {
  return withApiLogging("InstallReadyUpdate", undefined, () => InstallReadyUpdate());
}

export function openFooterAuthorHome() {
  return withApiLogging("OpenFooterAuthorHome", undefined, () => OpenFooterAuthorHome());
}

export function openModelConfig() {
  return withApiLogging("OpenModelConfigWindow", undefined, () => OpenModelConfigWindow());
}

export function openModelEditor(index, adapterJSON) {
  return withApiLogging("OpenModelEditorWindow", { index, adapterJSON }, () =>
    OpenModelEditorWindow(index, adapterJSON),
  );
}

export function getModelEditorContext() {
  return withApiLogging("GetModelEditorContext", undefined, () => GetModelEditorContext());
}

export function testModelAdapter(adapter) {
  return withApiLogging("TestModelAdapter", adapter, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.TestModelAdapter`, adapter),
  );
}

export function getModelAdapterTestResults() {
  return withApiLogging("GetModelAdapterTestResults", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetModelAdapterTestResults`),
  );
}



// === Pet Window API ===
const WINDOW_SERVICE_TARGET = "cursor/internal/bridge.WindowService";

export function togglePetWindow() {
  return withApiLogging("TogglePetWindow", undefined, () =>
    Call.ByName(`${WINDOW_SERVICE_TARGET}.TogglePetWindow`),
  );
}

export function isPetWindowVisible() {
  return withApiLogging("IsPetWindowVisible", undefined, () =>
    Call.ByName(`${WINDOW_SERVICE_TARGET}.IsPetWindowVisible`),
  );
}

export function switchPet(petId) {
  return withApiLogging("SwitchPet", { petId }, () =>
    Call.ByName(`${WINDOW_SERVICE_TARGET}.SwitchPet`, petId),
  );
}

export function setActivePet(petId) {
  return withApiLogging("SetActivePet", { petId }, () =>
    Call.ByName(`${WINDOW_SERVICE_TARGET}.SetActivePet`, petId),
  );
}

// === Pet Service API ===
const PET_SERVICE_TARGET = "cursor/internal/bridge.PetService";

export function scanPets() {
  return withApiLogging("ScanPets", undefined, () =>
    Call.ByName(`${PET_SERVICE_TARGET}.ScanPets`),
  );
}

export function openPetsDirectory() {
  return withApiLogging("OpenPetsDirectory", undefined, () =>
    Call.ByName(`${PET_SERVICE_TARGET}.OpenPetsDirectory`),
  );
}

