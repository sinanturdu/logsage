import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'

export const projectsKey = ['projects'] as const

export function useProjects() {
  return useQuery({
    queryKey: projectsKey,
    queryFn: ({ signal }) => api.listProjects(signal),
    select: (d) => d.projects,
  })
}

export function useCreateProject() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, slug }: { name: string; slug: string }) => api.createProject(name, slug),
    onSuccess: () => qc.invalidateQueries({ queryKey: projectsKey }),
  })
}
