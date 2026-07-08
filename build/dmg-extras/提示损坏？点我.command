#!/bin/bash
APP_PATH="/Applications/Cursor助手.app"

if [ ! -d "$APP_PATH" ]; then
  echo "请先把 Cursor助手 拖到“应用程序”目录后再运行本脚本。"
  read -r -p "按回车退出..."
  exit 1
fi

echo "正在移除隔离属性: $APP_PATH"
xattr -cr "$APP_PATH"
echo "处理完成，现在可以正常打开 Cursor助手。"
read -r -p "按回车退出..."
