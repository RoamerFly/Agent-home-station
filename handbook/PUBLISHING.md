# 维护这份手册

修改本目录的七篇 Markdown，正文同时用于仓库、Pages 和 Wiki。新增章节时更新 `scripts/build_handbook.py` 中的章节列表和首页目录。源码链接指向经过核对的功能版本。

在仓库根目录生成预览。Wiki 仅需 Python 标准库。Pages 还需要 Markdown 渲染库，可以在独立虚拟环境安装。

```sh
python -m pip install Markdown==3.8.2
python scripts/build_handbook.py pages temp/handbook-site
python scripts/build_handbook.py wiki temp/handbook-wiki
python -m http.server 8000 --bind 127.0.0.1 --directory temp/handbook-site
```

访问 http://127.0.0.1:8000/ ，检查导航、表格与代码块。更新线上教程时，分别检出项目的 `gh-pages` 分支和 `https://github.com/RoamerFly/Agent-home-station.wiki.git`，将对应的输出目录参数换为检出目录，审阅差异后正常提交并推送。Pages 的发布源为 `gh-pages` 根目录。Wiki 首次使用时需先在 GitHub 创建首页。生成器只写指定的手册文件；如删除或改名章节，请同时检查并清理原来的发布文件。
