from django.http import JsonResponse
from django.urls import path


def index(request):
    return JsonResponse({'app': 'rigger-django-starter', 'status': 'ok'})


urlpatterns = [
    path('', index),
]
