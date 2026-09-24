// @vitest-environment happy-dom
/**
 * 深度写作场景 Bug 验证测试
 * 测试标题+图片组合、高亮块叠加、格式混合等场景
 */
import { describe, expect, it } from "vitest";
import { editorHtmlToMarkdown } from "./main";
import { editorCalloutHtml } from "./editor-callout";
import { editorImageHtml } from "./editor-media";

function makeEditor(html: string): HTMLElement {
  const el = document.createElement("div");
  el.contentEditable = "true";
  el.innerHTML = html;
  return el;
}

describe("BUG-B01: 标题行内插入图片，# 前缀应被保留", () => {
  it("H1 内部含 figure.editor-image 时，# 前缀不应丢失", () => {
    const imageHtml = editorImageHtml("https://example.com/img.jpg", "图片说明");
    const html = `<h1>深度学习实战指南${imageHtml}</h1>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("H1+图片 实际输出:", md);
    expect(md).toContain("# 深度学习实战指南");
    expect(md).toContain("![图片说明]");
  });

  it("H2 内部含 figure 时，## 前缀不应丢失", () => {
    const imageHtml = editorImageHtml("https://example.com/photo.jpg", "示例截图");
    const html = `<h2>第一章：安装配置${imageHtml}</h2><p>正文内容</p>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("H2+图片 实际输出:", md);
    expect(md).toContain("## 第一章：安装配置");
    expect(md).toContain("![示例截图]");
  });

  it("H1 后紧跟 figure（分开的节点）时，两者都正确保留", () => {
    const imageHtml = editorImageHtml("https://example.com/cover.jpg", "封面图");
    const html = `<h1>文章标题</h1>${imageHtml}<p>正文</p>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("H1后图片 实际输出:", md);
    expect(md).toContain("# 文章标题");
    expect(md).toContain("![封面图]");
  });
});

describe("BUG-G01: 图片在 H1 内（formatBlock结果）不应产生空标题", () => {
  it("纯图片 figure 被包进 H1 时，不应生成空的 # 标题行", () => {
    const imageHtml = editorImageHtml("https://example.com/img.jpg", "图片");
    const html = `<h1>${imageHtml}<br></h1>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("图片在H1内 实际输出:", md);
    expect(md).toContain("![图片]");
    expect(md).not.toMatch(/^#\s*$/m);
  });
});

describe("Callout 内含 H2 标题 + 图片", () => {
  it("Callout 内 H2+图片，两者格式都应在 callout 指令内保留", () => {
    const imageHtml = editorImageHtml("https://example.com/callout-img.jpg", "高亮块图片");
    const calloutContent = `<h2>高亮块内的标题</h2>${imageHtml}<p>高亮块内文字</p>`;
    const html = `${editorCalloutHtml(calloutContent, "💡")}<h2>外部标题</h2>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("Callout+H2+图片 实际输出:", md);
    expect(md).toContain("callout");
    expect(md).toContain("高亮块内的标题");
    expect(md).toContain("高亮块图片");
    expect(md).toContain("## 外部标题");
  });

  it("Callout 内 H2 后接图片，H2 格式应保留", () => {
    const imageHtml = editorImageHtml("https://example.com/after.jpg", "标题后图片");
    const calloutContent = `<h2>高亮块标题</h2>${imageHtml}`;
    const editor = makeEditor(editorCalloutHtml(calloutContent, "⚠️"));
    const md = editorHtmlToMarkdown(editor);
    console.log("Callout内H2后图片 实际输出:", md);
    expect(md).toContain("高亮块标题");
    expect(md).toContain("标题后图片");
  });
});

describe("missingHeadings 假阳性：图片alt与标题文字相同", () => {
  it("H2 内含同名alt图片时，## 标题不应在末尾重复出现", () => {
    const imageHtml = editorImageHtml("https://example.com/dl.jpg", "深度学习");
    const html = `<h2>深度学习${imageHtml}</h2><p>正文段落</p>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("missingHeadings场景 实际输出:", md);
    const count = (md.match(/^## 深度学习/gm) ?? []).length;
    console.log(`"## 深度学习" 出现 ${count} 次（期望 1 次）`);
    expect(count).toBe(1);
  });
});

describe("格式叠加：颜色 + 粗体 + 斜体在标题内", () => {
  it("H1 内嵌 span.fp-color-red + strong + em，格式完整输出", () => {
    const html = `<h1><span class="fp-color-red"><strong><em>彩色加粗斜体标题</em></strong></span></h1>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("颜色+加粗+斜体+H1 实际输出:", md);
    expect(md).toMatch(/^# /m);
    expect(md).toContain("彩色加粗斜体标题");
  });

  it("段落内 fp-size-lg + fp-color-blue + strong 组合完整输出", () => {
    const html = `<p><span class="fp-size-lg"><span class="fp-color-blue"><strong>大号蓝色加粗</strong></span></span></p>`;
    const editor = makeEditor(html);
    const md = editorHtmlToMarkdown(editor);
    console.log("字号+颜色+加粗 实际输出:", md);
    expect(md).toContain("大号蓝色加粗");
    expect(md).toContain("fp-size-lg");
    expect(md).toContain("fp-color-blue");
  });
});

describe("嵌套 Callout（绕过UI限制时）不崩溃", () => {
  it("嵌套 callout 不抛异常，至少输出一层 callout 指令", () => {
    const inner = editorCalloutHtml("<p>内层</p>", "🔔");
    const outer = editorCalloutHtml(`<p>外层</p>${inner}`, "💡");
    const editor = makeEditor(outer);
    expect(() => editorHtmlToMarkdown(editor)).not.toThrow();
    const md = editorHtmlToMarkdown(editor);
    console.log("嵌套Callout 实际输出:", md);
    expect(md).toContain("callout");
  });
});
