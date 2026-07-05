import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import ModelEditor from "@/views/ModelEditor.vue";
import NewAPIAccount from "@/views/NewAPIAccount.vue";
import NewAPIModels from "@/views/NewAPIModels.vue";
import NewAPILogs from "@/views/NewAPILogs.vue";

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      component: Home,
      meta: { showIcon: true, title: "Cursor助手｜永久免费｜自定义API", directlyClose: false },
    },
    {
      path: "/model-config",
      component: ModelConfig,
      meta: { showIcon: false, title: "模型配置", directlyClose: true },
    },
    {
      path: "/model-editor",
      component: ModelEditor,
      meta: { showIcon: false, title: "模型编辑", directlyClose: true },
    },
    {
      path: "/newapi-account",
      component: NewAPIAccount,
      meta: { showIcon: false, title: "NewAPI 账号", directlyClose: true },
    },
    {
      path: "/newapi-models",
      component: NewAPIModels,
      meta: { showIcon: false, title: "导入 NewAPI 模型", directlyClose: true },
    },
    {
      path: "/newapi-logs",
      component: NewAPILogs,
      meta: { showIcon: false, title: "NewAPI 使用记录", directlyClose: true },
    },
  ],
});

export default router;
