import { createBrowserRouter } from 'react-router-dom'
import SetupPage from './routes/setup'

export const router = createBrowserRouter([
  {
    path: '/',
    element: <SetupPage />,
  },
  {
    path: '/setup',
    element: <SetupPage />,
  },
])
