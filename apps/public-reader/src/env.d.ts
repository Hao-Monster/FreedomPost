/// <reference types="astro/client" />

interface Window {
  $chatwoot?: {
    hasLoaded: boolean;
    toggle: (state?: "open" | "close") => void;
    setUser?: (identifier: string, attributes?: Record<string, string>) => void;
  };
  chatwootSDK?: {
    run: (options: { websiteToken: string; baseUrl: string }) => void;
  };
  __FREEDOMPOST_INITIAL__?: {
    slug: string;
    meta: {
      slug: string;
      title: string;
      createdAt: string;
      updatedAt: string;
      viewCount: number;
      commentCount: number;
      excerpt?: string;
    };
    toc: Array<{
      id: string;
      text: string;
      level: 1 | 2 | 3 | 4 | 5 | 6;
    }>;
  };
}


