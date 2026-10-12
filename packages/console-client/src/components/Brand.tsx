import { Trans } from '@lingui/react/macro';

export const Brand = () => (
  <span className="console-brand">
    <svg className="console-brand-mark" viewBox="0 0 28 28" aria-hidden="true">
      <rect x="2" y="2" width="24" height="24" rx="6" fill="currentColor" />
      <path d="M19 9h-8a3 3 0 0 0 0 6h6a2 2 0 0 1 0 4H9" fill="none" stroke="white" strokeWidth="2" strokeLinecap="round" />
    </svg>
    <span><Trans>Syntrix Console</Trans></span>
  </span>
);
