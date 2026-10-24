import { renderMarkdownArticle } from "@freedompost/renderer";
import type { PostListItem, SearchDocument } from "@freedompost/shared";
import { seedPosts } from "../data/seed-posts";

// Demo content is never emitted by a production build.
export const articles = (import.meta.env.DEV ? seedPosts : [])
  .map((post) => {
    const rendered = renderMarkdownArticle({
      slug: post.slug,
      title: post.title,
      markdown: post.markdown,
      createdAt: post.createdAt,
      updatedAt: post.updatedAt,
      viewCount: post.viewCount,
      commentCount: post.commentCount
    });

    return {
      ...post,
      ...rendered
    };
  })
  .sort((left, right) => right.createdAt.localeCompare(left.createdAt));

export const latestArticle = articles[0];

export function findArticle(slug: string) {
  return articles.find((article) => article.slug === slug);
}

export function toPostListItem(article: (typeof articles)[number]): PostListItem {
  return {
    slug: article.slug,
    title: article.title,
    createdAt: article.createdAt,
    updatedAt: article.updatedAt,
    viewCount: article.viewCount,
    commentCount: article.commentCount,
    excerpt: article.excerpt
  };
}

export function toSearchDocument(article: (typeof articles)[number]): SearchDocument {
  return {
    id: article.slug,
    slug: article.slug,
    title: article.title,
    body: article.searchText,
    excerpt: article.excerpt,
    updatedAt: article.updatedAt
  };
}
