import { RouterProvider } from 'react-router-dom';
import { router } from './router';
import { ToastProvider } from './components/ui';
import { SessionWarning } from './components/features/session';

function App() {
  return (
    <ToastProvider>
      <SessionWarning />
      <RouterProvider router={router} />
    </ToastProvider>
  );
}

export default App;
