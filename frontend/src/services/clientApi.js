import {
  GetState,
  LoadUserConfig,
  SaveUserConfig,
  StartProxy,
  StopProxy,
} from "@bindings/cursor/internal/bridge/proxyservice.js";
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
  TogglePetWindow,
  IsPetWindowVisible,
  OpenPetWindowIfClosed,
  SwitchPet,
  SetActivePet,
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

export function getOptimizationCostSummary() {
  return withApiLogging("GetOptimizationCostSummary", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetOptimizationCostSummary`),
  );
}

export function getAOSLastTraceSummary() {
  return withApiLogging("GetAOSLastTraceSummary", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetAOSLastTraceSummary`),
  );
}
export function getAOSExecutionTree(sessionID) {
  return withApiLogging("GetAOSExecutionTree", { sessionID }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetAOSExecutionTree`, sessionID),
  );
}
export function replayAOSTrace(sessionID) {
  return withApiLogging("ReplayAOSTrace", { sessionID }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ReplayAOSTrace`, sessionID),
  );
}

export function replayAOSNode(sessionID, nodeIndex) {
  return withApiLogging("ReplayAOSNode", { sessionID, nodeIndex }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ReplayAOSNode`, sessionID, nodeIndex),
  );
}

// recognizeAOSMembers asks the AOS Leader to read each member's name + prompt
// and infer routing tags. Tags are written back into the in-memory team
// profile; subsequent Leader planning reads only the short tags.
export function recognizeAOSMembers() {
  return withApiLogging("RecognizeAOSMembers", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.RecognizeAOSMembers`),
  );
}

// fetchModelsFromProvider calls the upstream /v1/models endpoint for the
// given provider config and returns { models: string[], error?: string }.
export function fetchModelsFromProvider(baseURL, apiKey, type) {
  return withApiLogging("FetchModelsFromProvider", { baseURL, type }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.FetchModelsFromProvider`, { baseURL, apiKey, type }),
  );
}

export function resetHomeMetrics() {
  return withApiLogging("ResetHomeMetrics", undefined, () => ResetHomeMetrics());
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

// === Tool Runtime API ===

export function listTools() {
  return withApiLogging("ListTools", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ListTools`),
  );
}

export function toggleTool(name, enabled) {
  return withApiLogging("ToggleTool", { name, enabled }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ToggleTool`, name, enabled),
  );
}

export function getToolCacheStats() {
  return withApiLogging("GetToolCacheStats", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetToolCacheStats`),
  );
}
export function getCacheStats() {
  return withApiLogging("GetCacheStats", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetCacheStats`),
  );
}
export function clearCache() {
  return withApiLogging("ClearCache", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ClearCache`),
  );
}

export function listMCPServers() {
  return withApiLogging("ListMCPServers", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ListMCPServers`),
  );
}

export function toggleMCPServer(server, enabled) {
  return withApiLogging("ToggleMCPServer", { server, enabled }, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ToggleMCPServer`, server, enabled),
  );
}
export function clearToolCache() {
  return withApiLogging("ClearToolCache", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ClearToolCache`),
  );
}


// === Pet Window API ===
// R1: 改用 windowservice.js 的强类型 ByID binding，原 Call.ByName 在
// Wails v3 下因服务名映射缺失会静默 reject，导致桌宠开关完全无响应。

export function togglePetWindow() {
  return withApiLogging("TogglePetWindow", undefined, () => TogglePetWindow());
}

export function isPetWindowVisible() {
  return withApiLogging("IsPetWindowVisible", undefined, () => IsPetWindowVisible());
}

export function openPetWindowIfClosed() {
  return withApiLogging("OpenPetWindowIfClosed", undefined, () => OpenPetWindowIfClosed());
}

export function switchPet(petId) {
  return withApiLogging("SwitchPet", { petId }, () => SwitchPet(petId));
}

export function setActivePet(petId) {
  return withApiLogging("SetActivePet", { petId }, () => SetActivePet(petId));
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

