<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import { localized } from "@/i18n/runtime";
import { showModal } from "@/composables/useModal";
import {
  appState,
  openModelConfigWindow,
  persistUserConfig,
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  toUserError,
} from "@/state/appState";
import { onMounted } from "vue";

const L = {
  serviceError: localized("a1b2c3d4e5f6006b", "Service Error"),
  saveFailed: localized("a1b2c3d4e5f60074", "Save failed"),
  notice: localized("a1b2c3d4e5f60075", "Notice"),
  localSettingsSaved: localized("a1b2c3d4e5f60076", "Local settings saved"),
  openFailed: localized("a1b2c3d4e5f6006c", "Failed to open"),
  localSettings: localized("a1b2c3d4e5f6002b", "Local Settings"),
  localSettingsDesc: localized("49c5e6e7b8a3f1d2", "Configure routing mode and model channels; logs are in"),
  saving: localized("a1b2c3d4e5f60068", "Saving..."),
  saveConfig: localized("7e9e334aeb0bdc07", "Save Settings"),
  routingMode: localized("a1b2c3d4e5f6002f", "Routing Mode"),
  routingModeDesc: localized("a1b2c3d4e5f60030", "Control whether whitelist requests go through local service or upstream"),
  selectMode: localized("a1b2c3d4e5f60031", "Select mode"),
  interfaceLanguage: localized("a1b2c3d4e5f60032", "Interface Language"),
  interfaceLanguageDesc: localized("a1b2c3d4e5f60033", "Switch display language. Setting takes effect immediately."),
  modelSettings: localized("a1b2c3d4e5f60034", "Model Settings"),
  modelSettingsDesc: localized("a1b2c3d4e5f60035", "{0} model adapters configured"),
  openModelSettings: localized("a1b2c3d4e5f60036", "Open Model Settings"),
};

const routeModeOptions = ROUTE_MODE_OPTIONS;

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || L.serviceError).trim() || L.serviceError,
  });
}

async function handleSaveConfig() {
  const result = await persistUserConfig();
  if (!result.ok) {
    await showActionError(L.saveFailed, result.error);
    return;
  }
  await showModal({
    title: L.notice,
    content: L.localSettingsSaved,
  });
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError(L.openFailed, toUserError(error));
  }
}

onMounted(async () => {
  await reloadUserConfig().catch(() => {});
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">{{ L.localSettings }}</h2>
          <div class="text-sm text-[#a3a3a3]">
            {{ L.localSettingsDesc }} <code>~/.cursor-local-assistant-v2/logs/</code>
          </div>
        </div>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveConfig">
          {{ appState.configSaving ? L.saving : L.saveConfig }}
        </Button>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">{{ L.routingMode }}</h2>
          <div class="text-sm text-[#a3a3a3]">
            {{ L.routingModeDesc }}
          </div>
        </div>
        <div class="w-[220px] max-w-full">
          <Select
            v-model="appState.routingMode"
            :options="routeModeOptions"
            :placeholder="L.selectMode"
          />
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">{{ L.interfaceLanguage }}</h2>
          <div class="text-sm text-[#a3a3a3]">
            {{ L.interfaceLanguageDesc }}
          </div>
        </div>
        <LocaleSelect wrapper-class="w-[220px] max-w-full" />
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">{{ L.modelSettings }}</h2>
          <div class="text-sm text-[#a3a3a3]">
            {{ L.modelSettingsDesc.toString(appState.modelAdapters.length) }}
          </div>
        </div>
        <Button variant="primary" @click="handleOpenModelConfig">{{ L.openModelSettings }}</Button>
      </div>
    </Card>
  </div>
</template>
