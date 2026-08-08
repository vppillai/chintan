import { RouterProvider, createBrowserRouter } from 'react-router';

import { routes } from '@/app/router.tsx';
import { ThemeProvider } from '@/theme/ThemeProvider.tsx';

/**
 * `basename` comes from Vite's `BASE_URL`, which is set at build time from
 * `VITE_BASE`. That is what lets one bundle serve from a GitHub Pages
 * per-instance sub-path without any runtime path sniffing.
 */
const router = createBrowserRouter(routes, {
  basename: import.meta.env.BASE_URL.replace(/\/$/, '') || '/',
});

export function App() {
  return (
    <ThemeProvider>
      <RouterProvider router={router} />
    </ThemeProvider>
  );
}
