'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';

const LINKS = [
  { href: '/', label: 'Overview' },
  { href: '/incidents', label: 'Incidents' },
  { href: '/settings', label: 'Settings' },
] as const;

export function Sidebar() {
  const pathname = usePathname();

  return (
    <aside className="hidden w-56 shrink-0 border-r border-base-border bg-base-surface/60 md:block">
      <div className="flex h-14 items-center gap-2 border-b border-base-border px-5">
        <span className="grid h-6 w-6 place-items-center rounded-md bg-accent text-[11px] font-bold text-white">
          kl
        </span>
        <span className="text-sm font-semibold tracking-tight">kubelens</span>
      </div>

      <nav className="p-3">
        <ul className="space-y-1">
          {LINKS.map((link) => {
            // Exact match for the root, prefix elsewhere, so an incident detail
            // page still highlights Incidents.
            const active =
              link.href === '/' ? pathname === '/' : pathname.startsWith(link.href);

            return (
              <li key={link.href}>
                <Link
                  href={link.href}
                  aria-current={active ? 'page' : undefined}
                  className={`block rounded-lg px-3 py-2 text-sm transition ${
                    active
                      ? 'bg-accent-soft text-base-text'
                      : 'text-base-muted hover:bg-base-raised hover:text-base-text'
                  }`}
                >
                  {link.label}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
    </aside>
  );
}

/** MobileNav replaces the sidebar below the md breakpoint. */
export function MobileNav() {
  const pathname = usePathname();

  return (
    <nav className="flex gap-1 border-b border-base-border bg-base-surface/60 px-3 py-2 md:hidden">
      {LINKS.map((link) => {
        const active = link.href === '/' ? pathname === '/' : pathname.startsWith(link.href);
        return (
          <Link
            key={link.href}
            href={link.href}
            className={`rounded-lg px-3 py-1.5 text-sm ${
              active ? 'bg-accent-soft text-base-text' : 'text-base-muted'
            }`}
          >
            {link.label}
          </Link>
        );
      })}
    </nav>
  );
}
