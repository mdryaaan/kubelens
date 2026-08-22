'use client';

import { useCallback, useEffect, useState } from 'react';

export type Theme = 'dark' | 'light';

const STORAGE_KEY = 'kubelens-theme';

/**
 * Reads and writes the theme, defaulting to dark.
 *
 * The attribute is set on <html> rather than a class on a provider, so the
 * inline script in the layout can apply it before first paint — a dashboard
 * that flashes white before going dark is worse than one that is simply light.
 */
export function useTheme() {
  const [theme, setThemeState] = useState<Theme>('dark');
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    const initial: Theme = stored === 'light' ? 'light' : 'dark';
    setThemeState(initial);
    setMounted(true);
  }, []);

  const setTheme = useCallback((next: Theme) => {
    setThemeState(next);
    document.documentElement.setAttribute('data-theme', next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // A browser with storage disabled still gets the theme for this session.
    }
  }, []);

  const toggle = useCallback(() => {
    setTheme(theme === 'dark' ? 'light' : 'dark');
  }, [theme, setTheme]);

  return { theme, setTheme, toggle, mounted };
}
