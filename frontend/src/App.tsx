import { RouterProvider, createBrowserRouter } from 'react-router';

import { ApiProvider } from '@/api/ApiProvider.tsx';
import { routerBasename, scopedEntryUrl } from '@/app/basePath.ts';
import { routes } from '@/app/router.tsx';
import { ThemeProvider } from '@/theme/ThemeProvider.tsx';

/**
 * `basename` comes from Vite's `BASE_URL`, which is set at build time from
 * `VITE_BASE`. That is what lets one bundle serve from a GitHub Pages
 * per-instance sub-path without any runtime path sniffing.
 *
 * The trailing slash is kept — see `basePath.ts` for why stripping it put
 * every in-app URL outside the installed app's scope.
 */
function createAppRouter() {
  const base = import.meta.env.BASE_URL;
  // A document loaded at the scope's path without its slash is moved inside
  // the scope before the router reads the address bar.
  const entry = scopedEntryUrl(base, window.location);
  if (entry) window.history.replaceState(window.history.state, '', entry);
  return createBrowserRouter(routes, { basename: routerBasename(base) });
}

const router = createAppRouter();

export function App() {
  return (
    <ThemeProvider>
      <ApiProvider>
        <RouterProvider router={router} />
      </ApiProvider>
    </ThemeProvider>
  );
}
