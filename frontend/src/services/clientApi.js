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
import { GetHomeMetricsSummary } from "@bindings/cursor/internal/bridge/metricsservice.js";
import {
  CheckForUpdates,
  GetAppVersion,
  GetFooterAuthorInfo,
  GetModelEditorContext,
  InstallReadyUpdate,
  OpenConfigWindow,
  OpenFooterAuthorHome,
  OpenHistoryWindow,
  OpenModelConfigWindow,
  OpenModelEditorWindow,
  OpenNewAPIAccountWindow,
  OpenNewAPILogsWindow,
  OpenNewAPIModelsWindow,
} from "@bindings/cursor/internal/bridge/windowservice.js";
import { Call } from "@wailsio/runtime";

const API_LOG_PREFIX = "[clientApi]";
const PROXY_SERVICE_NAME = "cursor/internal/bridge.ProxyService";
const NEWAPI_SERVICE_NAME = "cursor/internal/bridge.NewAPIService";

function logSuccess(name, payload, result) {
  console.log(`${API_LOG_PREFIX} ${name} response`, {
    payload,
    result,
  });
}

function logError(name, payload, error) {
  console.error(`${API_LOG_PREFIX} ${name} error`, {
    payload,
    error,
  });
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

export function getAdRuntime() {
  return GetAdRuntime();
}

export function openAdExternalURL(url) {
  return OpenAdExternalURL(url);
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

export function openNewAPIAccount() {
  return withApiLogging("OpenNewAPIAccountWindow", undefined, () => OpenNewAPIAccountWindow());
}

export function openNewAPIModels() {
  return withApiLogging("OpenNewAPIModelsWindow", undefined, () => OpenNewAPIModelsWindow());
}

export function openNewAPILogs() {
  return withApiLogging("OpenNewAPILogsWindow", undefined, () => OpenNewAPILogsWindow());
}

export function getModelEditorContext() {
  return withApiLogging("GetModelEditorContext", undefined, () => GetModelEditorContext());
}

export function testModelAdapter(adapter) {
  return Call.ByName(`${PROXY_SERVICE_NAME}.TestModelAdapter`, adapter).then(
    (result) => {
      logSuccess("TestModelAdapter", adapter, result);
      return result;
    },
    (error) => {
      logError("TestModelAdapter", adapter, error);
      throw error;
    },
  );
}

export function getModelAdapterTestResults() {
  return withApiLogging("GetModelAdapterTestResults", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetModelAdapterTestResults`),
  );
}

// --- NewAPI 集成 ---

// 个人令牌登录
export function newAPITokenLogin(payload) {
  return withApiLogging("NewAPITokenLogin", payload, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPITokenLogin`, payload),
  );
}

export function newAPILogin(payload) {
  return withApiLogging("NewAPILogin", payload, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPILogin`, payload),
  );
}

export function newAPIGetStatus() {
  return withApiLogging("NewAPIGetStatus", undefined, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPIGetStatus`),
  );
}

export function newAPIGetLogs(payload) {
  return withApiLogging("NewAPIGetLogs", payload, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPIGetLogs`, payload),
  );
}

export function newAPIGetModels() {
  return withApiLogging("NewAPIGetModels", undefined, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPIGetModels`),
  );
}

export function newAPIImportModels(payload) {
  return withApiLogging("NewAPIImportModels", payload, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPIImportModels`, payload),
  );
}

export function newAPILogout() {
  return withApiLogging("NewAPILogout", undefined, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPILogout`),
  );
}

export function newAPIOpenTopup() {
  return withApiLogging("NewAPIOpenTopup", undefined, () =>
    Call.ByName(`${NEWAPI_SERVICE_NAME}.NewAPIOpenTopup`),
  );
}
