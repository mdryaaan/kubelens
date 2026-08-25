import type { Metadata, Viewport } from 'next';

import { MobileNav, Sidebar } from '@/components/layout/Sidebar';

import './globals.css';

export const metadata: Metadata = {
  title: 'kubelens',
  description:
    'Watch a Kubernetes cluster, detect real failures, and explain them with cited evidence.',
};

export const viewport: Viewport = {
  themeColor: '#0a0b0f',
};

/**
 * Applied before first paint so a dark-mode user never sees a white flash.
 * Reading localStorage in an effect is one render too late for that.
 */
const THEME_SCRIPT = `
(function () {
  try {
    var stored = localStorage.getItem('kubelens-theme');
    document.documentElement.setAttribute('data-theme', stored === 'light' ? 'light' : 'dark');
  } catch (e) {
    document.documentElement.setAttribute('data-theme', 'dark');
  }
})();
`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" data-theme="dark" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_SCRIPT }} />
      </head>
      <body className="min-h-screen bg-base-bg text-base-text antialiased">
        <div className="flex min-h-screen">
          <Sidebar />
          <div className="flex min-w-0 flex-1 flex-col">
            <MobileNav />
            {children}
          </div>
        </div>
      </body>
    </html>
  );
}
