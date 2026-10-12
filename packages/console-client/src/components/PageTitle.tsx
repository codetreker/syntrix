import { useLingui } from '@lingui/react/macro';
import { useEffect } from 'react';

export const PageTitle = ({ title }: { title: string }) => {
  const { t } = useLingui();
  const documentTitle = t`${title} · Syntrix Console`;
  useEffect(() => { document.title = documentTitle; }, [documentTitle]);
  return null;
};
