package observe

import (
	"fmt"
	"math/rand"

	"git.vshn.net/vshn/baas/log"
	"git.vshn.net/vshn/baas/service"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

type topic string

type PodState struct {
	BaasID     string
	State      string
	Repository string
}

type Broker struct {
	subscribers map[topic][]Subscriber
}

type Subscriber struct {
	CH chan PodState
	id int // ID has to be uniqe within a topic
}

// WatchObjects contains everything needed to watch jobs. It can also hold
// functions that get triggered during the equivalent event (success,fail,running)
type WatchObjects struct {
	Logger      log.Logger
	Job         *batchv1.Job
	Locker      Locker
	JobType     JobType
	Successfunc func(message PodState)
	Failedfunc  func(message PodState)
	Runningfunc func(message PodState)
	Defaultfunc func(message PodState)
}

func (s *Subscriber) update(state PodState) {
	s.CH <- state
}

func newBroker() *Broker {
	return &Broker{
		subscribers: make(map[topic][]Subscriber, 0),
	}
}

func (b *Broker) Subscribe(topicName string) (*Subscriber, error) {
	if subs, ok := b.subscribers[topic(topicName)]; !ok {
		tmpSlice := make([]Subscriber, 0)

		tmpSub := Subscriber{
			CH: make(chan PodState, 0),
			id: rand.Int(),
		}

		tmpSlice = append(tmpSlice, tmpSub)

		b.subscribers[topic(topicName)] = tmpSlice

		return &tmpSub, nil

	} else {
		exists := true
		for exists {
			newID := rand.Int()
			exists = false
			for i := range subs {
				if subs[i].id == newID {
					exists = true
					break
				}
			}
			if !exists {
				tmpSub := Subscriber{
					id: newID,
					CH: make(chan PodState, 0),
				}
				subs = append(subs, tmpSub)
				b.subscribers[topic(topicName)] = subs
				return &tmpSub, nil
			}
		}
		return nil, fmt.Errorf("Could not register")
	}
}

func (b *Broker) Unsubscribe(topicName string, subscriber *Subscriber) {
	if subs, ok := b.subscribers[topic(topicName)]; ok {
		deleteIndex := 0
		for i := range subs {
			if subs[i].id == subscriber.id {
				deleteIndex = i
			}
		}
		close(subs[deleteIndex].CH)
		b.subscribers[topic(topicName)] = append(subs[:deleteIndex], subs[deleteIndex+1:]...)
	}
}

func (b *Broker) Notify(topicName string, state PodState) error {
	if subs, ok := b.subscribers[topic(topicName)]; ok {
		for i := range subs {
			go subs[i].update(state)
		}
	}
	return nil
}

func (s *Subscriber) WatchLoop(watch WatchObjects) {

	running := false
	backendString := service.GetRepository(&corev1.Pod{Spec: watch.Job.Spec.Template.Spec})

	for message := range s.CH {
		switch message.State {
		case string(corev1.PodFailed):
			watch.Logger.Errorf("Pod %v in namespace %v failed", watch.Job.GetName(), watch.Job.GetNamespace())
			if watch.Failedfunc != nil {
				watch.Failedfunc(message)
			}
			watch.Locker.Decrement(backendString, watch.JobType)
			return
		case string(corev1.PodSucceeded):
			watch.Logger.Infof("Pod %v in namespace %v finished successfully", watch.Job.GetName(), watch.Job.GetNamespace())
			if watch.Successfunc != nil {
				watch.Successfunc(message)
			}
			watch.Locker.Decrement(backendString, watch.JobType)
			return
		case string(corev1.PodRunning):
			watch.Logger.Infof("Pod %v in namespace %v is still running", watch.Job.GetName(), watch.Job.GetNamespace())
			if !running {
				watch.Locker.Increment(backendString, watch.JobType)
				running = true
			}
			if watch.Runningfunc != nil {
				watch.Runningfunc(message)
			}
		default:
			watch.Logger.Infof("Pod state for job %v is: %v", watch.Job.Name, message.State)
			if watch.Defaultfunc != nil {
				watch.Defaultfunc(message)
			}
		}
	}
}